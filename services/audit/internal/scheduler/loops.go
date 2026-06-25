package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// FUT-019 Phase 2 — scheduler + dispatcher loops.
//
// Two loops live behind a single Runner. The Runner is the public
// surface; cmd/server/main.go starts it once at boot and the loops
// run for the process lifetime.
//
//   Scheduler   ticks every SchedulerInterval (default 1h). For each
//               active tenant + each registered category, computes
//               whether the cadence window has elapsed and calls
//               repository.ScheduleNotification. The unique index on
//               (tenant_id, category, due_at) makes the call
//               idempotent — duplicates are a no-op.
//
//   Dispatcher  ticks every DispatcherInterval (default 1m). Drains
//               up to DispatcherBatch rows from
//               scheduled_notifications via ClaimDueNotifications,
//               renders each via the category Registry, writes one
//               audit_events row per recipient (currently just one
//               row per tenant — the bell read path filters per-user
//               via user_notification_preferences), then marks the
//               scheduled row delivered. Failures bump attempts +
//               flip back to pending; attempts=3 → status='failed'.

// RunnerConfig carries the tunables every loop reads. Zero values
// fall back to safe production defaults.
type RunnerConfig struct {
	SchedulerInterval   time.Duration
	DispatcherInterval  time.Duration
	DispatcherBatch     int
	StuckInProgressMax  time.Duration
	ActiveTenantsWindow time.Duration
	// ActorID is the audit_events.actor_id value the dispatcher writes
	// for scheduled notifications. Defaults to "system".
	ActorID string
}

func (c *RunnerConfig) defaults() {
	if c.SchedulerInterval == 0 {
		c.SchedulerInterval = time.Hour
	}
	if c.DispatcherInterval == 0 {
		c.DispatcherInterval = time.Minute
	}
	if c.DispatcherBatch == 0 {
		c.DispatcherBatch = 10
	}
	if c.StuckInProgressMax == 0 {
		c.StuckInProgressMax = 5 * time.Minute
	}
	if c.ActiveTenantsWindow == 0 {
		c.ActiveTenantsWindow = 30 * 24 * time.Hour
	}
	if c.ActorID == "" {
		c.ActorID = "system"
	}
}

// Runner orchestrates the scheduler + dispatcher.
type Runner struct {
	repo       *repository.Repository
	categories []Category
	cfg        RunnerConfig
}

// New returns a Runner ready to Start. Pass the categories the
// deployment cares about — typically Registry() from categories.go.
func New(repo *repository.Repository, categories []Category, cfg RunnerConfig) *Runner {
	cfg.defaults()
	return &Runner{repo: repo, categories: categories, cfg: cfg}
}

// Start launches both loops. Blocks until ctx is cancelled. Errors
// from individual ticks log + continue — the loops are best-effort
// background workers, not a critical path.
func (r *Runner) Start(ctx context.Context) {
	// One-off catch-up on boot: run both loops once immediately so a
	// freshly-deployed pod doesn't wait an hour to schedule the first
	// monthly category.
	r.runSchedulerTick(ctx)
	r.runDispatcherTick(ctx)

	schedulerTicker := time.NewTicker(r.cfg.SchedulerInterval)
	dispatcherTicker := time.NewTicker(r.cfg.DispatcherInterval)
	defer schedulerTicker.Stop()
	defer dispatcherTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-schedulerTicker.C:
			r.runSchedulerTick(ctx)
		case <-dispatcherTicker.C:
			r.runDispatcherTick(ctx)
		}
	}
}

// ── Scheduler ────────────────────────────────────────────────────────

func (r *Runner) runSchedulerTick(ctx context.Context) {
	tenants, err := r.repo.ListActiveTenants(ctx, r.cfg.ActiveTenantsWindow)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 scheduler: list active tenants failed",
			"err", err)
		return
	}
	now := time.Now().UTC()
	totalNew := 0
	for _, tenantID := range tenants {
		for _, cat := range r.categories {
			created, err := r.scheduleIfDue(ctx, tenantID, cat, now)
			if err != nil {
				slog.WarnContext(ctx, "FUT-019 scheduler: schedule failed",
					"err", err, "tenant_id", tenantID, "category", cat.Name())
				continue
			}
			if created {
				totalNew++
			}
		}
	}
	if totalNew > 0 {
		slog.InfoContext(ctx, "FUT-019 scheduler tick",
			"tenants", len(tenants), "categories", len(r.categories),
			"newly_scheduled", totalNew)
	}
}

// scheduleIfDue computes whether the cadence window has elapsed for
// (tenant, category) and inserts a row if so. The repository call is
// idempotent via the unique index so this can run safely even if
// LastScheduledAt is racy.
//
// due_at is truncated to the hour so the unique index lookup is
// stable across retries that fire seconds apart.
func (r *Runner) scheduleIfDue(
	ctx context.Context,
	tenantID uuid.UUID,
	cat Category,
	now time.Time,
) (bool, error) {
	last, err := r.repo.LastScheduledAt(ctx, tenantID, cat.Name())
	if err != nil {
		return false, fmt.Errorf("last scheduled at: %w", err)
	}
	cadence := cat.Cadence()
	if cadence <= 0 {
		return false, fmt.Errorf("category %s returned non-positive cadence", cat.Name())
	}
	if !last.IsZero() && now.Sub(last) < cadence {
		return false, nil // not yet due
	}
	dueAt := now.Truncate(time.Hour)
	payload, err := cat.Build(tenantID, now)
	if err != nil {
		return false, fmt.Errorf("build payload: %w", err)
	}
	return r.repo.ScheduleNotification(ctx, tenantID, cat.Name(), dueAt, payload)
}

// ── Dispatcher ───────────────────────────────────────────────────────

func (r *Runner) runDispatcherTick(ctx context.Context) {
	// Crash-recovery sweep first — flips stuck in_progress rows back
	// so they don't permanently silence a tenant's notifications.
	if reverted, err := r.repo.RevertStuckInProgress(ctx, r.cfg.StuckInProgressMax); err != nil {
		slog.WarnContext(ctx, "FUT-019 dispatcher: revert stuck failed", "err", err)
	} else if reverted > 0 {
		slog.InfoContext(ctx, "FUT-019 dispatcher: reverted stuck in_progress",
			"count", reverted)
	}

	now := time.Now().UTC()
	claimed, err := r.repo.ClaimDueNotifications(ctx, now, r.cfg.DispatcherBatch)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 dispatcher: claim failed", "err", err)
		return
	}
	if len(claimed) == 0 {
		return
	}

	categoryByName := make(map[string]Category, len(r.categories))
	for _, c := range r.categories {
		categoryByName[c.Name()] = c
	}

	delivered := 0
	failed := 0
	for _, sn := range claimed {
		if err := r.dispatchOne(ctx, sn, categoryByName); err != nil {
			slog.WarnContext(ctx, "FUT-019 dispatcher: deliver failed",
				"err", err, "id", sn.ID, "category", sn.Category, "attempt", sn.Attempts)
			if markErr := r.repo.MarkFailed(ctx, sn.ID, err.Error()); markErr != nil {
				slog.ErrorContext(ctx, "FUT-019 dispatcher: mark failed errored",
					"err", markErr, "id", sn.ID)
			}
			failed++
			continue
		}
		if markErr := r.repo.MarkDelivered(ctx, sn.ID); markErr != nil {
			slog.ErrorContext(ctx, "FUT-019 dispatcher: mark delivered errored",
				"err", markErr, "id", sn.ID)
			failed++
			continue
		}
		delivered++
	}
	slog.InfoContext(ctx, "FUT-019 dispatcher tick",
		"claimed", len(claimed), "delivered", delivered, "failed", failed)
}

func (r *Runner) dispatchOne(
	ctx context.Context,
	sn *repository.ScheduledNotification,
	categoryByName map[string]Category,
) error {
	cat, ok := categoryByName[sn.Category]
	if !ok {
		return fmt.Errorf("unknown category %q", sn.Category)
	}
	rendered, err := cat.Render(sn.Payload)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	// Audit-event envelope. metadata.raw carries the rendered title /
	// summary / link + the original payload so the notifications-bell
	// handler can pass-through without re-parsing per category.
	envelope := struct {
		Category    string            `json:"category"`
		Title       string            `json:"title"`
		Summary     string            `json:"summary"`
		Link        string            `json:"link"`
		ExtraMeta   map[string]string `json:"metadata,omitempty"`
		ScheduledID string            `json:"scheduled_id"`
	}{
		Category:    sn.Category,
		Title:       rendered.Title,
		Summary:     rendered.Summary,
		Link:        rendered.Link,
		ExtraMeta:   rendered.Metadata,
		ScheduledID: sn.ID.String(),
	}
	rawEnv, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	// Wrap in the audit_events.metadata.raw shape the
	// notifications-bell handler expects.
	wrapped, err := json.Marshal(struct {
		Raw json.RawMessage `json:"raw"`
	}{Raw: rawEnv})
	if err != nil {
		return fmt.Errorf("wrap envelope: %w", err)
	}
	if err := r.repo.Insert(ctx, &repository.AuditEvent{
		TenantID:   sn.TenantID,
		ActorID:    r.cfg.ActorID,
		ActorType:  "system",
		Action:     "notification.scheduled",
		Resource:   sn.Category,
		Outcome:    "success",
		Metadata:   wrapped,
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// ErrUnknownCategory is returned by dispatchOne when a row's category
// name has no entry in the registry. Surfaced as a sentinel so the
// dispatcher loop can decide whether to mark failed (yes — a row no
// one knows how to render is permanently broken).
var ErrUnknownCategory = errors.New("unknown category")
