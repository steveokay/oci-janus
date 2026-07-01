// Package worker — access_review.go implements the FUT-004 weekly
// access-review worker.
//
// Flow per tick (default: weekly):
//  1. Enumerate every tenant that has any active api_keys row (spec
//     Feature 4 — we don't restrict to tenants with a configured
//     idle_revoke_days because the default threshold applies to every
//     workspace).
//  2. For each tenant:
//     a. Acquire pg_try_advisory_lock keyed on the tenant id (FNV-64a
//        hash, salted with "access-review:" so this lock never collides
//        with FUT-003's idle-revoke lock on the same tenant).
//     b. Load the tenant's policy → resolve the idle threshold.
//     c. Call AccessReviewService.ListStaleKeys (which handles the
//        default-threshold fallback + snoozed-key exclusion).
//     d. For each stale key: emit auth.access_review_due with the
//        reason from the suggested-action heuristic.
//
// The worker is NUDGE-ONLY — spec Decision #4. It never revokes; that
// job belongs to FUT-003's idle_revoke worker. The audit consumer's
// mapEvent case for RoutingAccessReviewDue is responsible for surfacing
// the notification bell entry (via the FUT-019 Phase 1 pipeline).
//
// Failure semantics:
//   - Any error on tenant N never blocks tenants N+1..M (log + continue).
//   - A publish failure per-key logs + continues so a broker hiccup
//     doesn't skip the rest of the tenant's stale keys.
//
// Fake-clock friendly: `now func() time.Time` and `tickPeriod` are
// injectable for tests.
package worker

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// AccessReviewPublisher is the narrow interface AccessReview uses to
// emit auth.access_review.due events. Kept small so tests can supply a
// fake without pulling the full RabbitMQ publisher stack.
type AccessReviewPublisher interface {
	// PublishAccessReviewDue publishes an auth.access_review.due
	// envelope carrying the AccessReviewDuePayload for one stale key.
	// Errors are surfaced so the worker can log per-key.
	PublishAccessReviewDue(ctx context.Context, tenantID uuid.UUID, payload events.AccessReviewDuePayload) error
}

// accessReviewSvc is the narrow service interface the worker uses.
// Satisfied by *service.AccessReviewService.
type accessReviewSvc interface {
	ListStaleKeys(ctx context.Context, tenantID uuid.UUID) ([]service.StaleKeyView, error)
}

// tenantEnumerator is the narrow repository interface used to list every
// tenant with active keys. Satisfied by a helper on *repository.APIKeyRepository
// (defined below via a small wrapper type).
type tenantEnumerator interface {
	ListTenantsWithActiveKeys(ctx context.Context) ([]uuid.UUID, error)
}

// AccessReview is the background worker. Constructed at server startup;
// safe to call Run in a goroutine and cancel via context.
type AccessReview struct {
	pool       *pgxpool.Pool
	svc        accessReviewSvc
	tenants    tenantEnumerator
	pub        AccessReviewPublisher
	now        func() time.Time
	logger     *slog.Logger
	tickPeriod time.Duration
}

// NewAccessReview constructs an AccessReview worker. pub may be nil
// (no broker) — the worker's Tick then does zero visible work (the
// audit event is the entire product of a tick). logger nil falls back
// to slog.Default. tickPeriod defaults to one week.
func NewAccessReview(pool *pgxpool.Pool, svc accessReviewSvc, tenants tenantEnumerator, pub AccessReviewPublisher, logger *slog.Logger) *AccessReview {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccessReview{
		pool:       pool,
		svc:        svc,
		tenants:    tenants,
		pub:        pub,
		now:        time.Now,
		logger:     logger,
		tickPeriod: 7 * 24 * time.Hour,
	}
}

// WithClock replaces the wall-clock reader. Chainable builder.
func (w *AccessReview) WithClock(fn func() time.Time) *AccessReview {
	if fn != nil {
		w.now = fn
	}
	return w
}

// WithTickPeriod replaces the default weekly cadence (tests use short
// values so a full loop runs inside a bounded context).
func (w *AccessReview) WithTickPeriod(d time.Duration) *AccessReview {
	if d > 0 {
		w.tickPeriod = d
	}
	return w
}

// Run blocks until ctx is cancelled. Ticks at the configured cadence
// (weekly by default; overridable via WithTickPeriod). An IMMEDIATE first
// tick fires so a fresh boot doesn't leave the review queue empty for up
// to a week before the sweeper takes its first pass.
func (w *AccessReview) Run(ctx context.Context) {
	w.tickOnce(ctx)
	ticker := time.NewTicker(w.tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tickOnce(ctx)
		}
	}
}

// TickOnce runs one sweep. Exposed for testing so tests can drive the
// loop without spinning a goroutine + ticker.
func (w *AccessReview) TickOnce(ctx context.Context) {
	w.tickOnce(ctx)
}

// tickOnce enumerates tenants and dispatches per-tenant work.
func (w *AccessReview) tickOnce(ctx context.Context) {
	tenantIDs, err := w.tenants.ListTenantsWithActiveKeys(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "access_review: list tenants failed", "err", err)
		return
	}
	for _, tenantID := range tenantIDs {
		w.tickTenant(ctx, tenantID)
	}
}

// tickTenant handles ONE tenant per invocation. pg_try_advisory_lock
// prevents multi-replica double-work: if another auth replica already
// holds the lock, we skip immediately (the other replica will do the
// work on this tick).
//
// All errors log-and-continue — this loop must not starve other tenants.
func (w *AccessReview) tickTenant(ctx context.Context, tenantID uuid.UUID) {
	// Acquire a session-scoped advisory lock. Advisory locks are per-
	// session in Postgres, so we pin a single connection — if the pool
	// hands us a different connection for the unlock, the lock stays
	// held until the connection is closed.
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "access_review: acquire conn failed", "tenant_id", tenantID, "err", err)
		return
	}
	defer conn.Release()

	key := accessReviewLockKey(tenantID)
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&locked); err != nil {
		w.logger.WarnContext(ctx, "access_review: lock query failed", "tenant_id", tenantID, "err", err)
		return
	}
	if !locked {
		// Another replica already holds the tenant lock — skip silently.
		return
	}
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			w.logger.WarnContext(ctx, "access_review: unlock failed", "tenant_id", tenantID, "err", err)
		}
	}()

	views, err := w.svc.ListStaleKeys(ctx, tenantID)
	if err != nil {
		w.logger.WarnContext(ctx, "access_review: list stale keys failed", "tenant_id", tenantID, "err", err)
		return
	}
	for _, v := range views {
		w.emit(ctx, tenantID, v)
	}
}

// emit publishes an auth.access_review_due event carrying the reason
// from the suggested-action heuristic. Best-effort per key so a broker
// hiccup doesn't skip the remaining stale keys in the sweep.
func (w *AccessReview) emit(ctx context.Context, tenantID uuid.UUID, v service.StaleKeyView) {
	if w.pub == nil {
		return
	}
	// DaysIdle: computed only when we have a last_used_at we can subtract from.
	var daysIdle int32
	if v.Key.LastUsedAt != nil {
		delta := w.now().Sub(*v.Key.LastUsedAt) / (24 * time.Hour)
		if delta > 0 {
			daysIdle = int32(delta)
		}
	}
	payload := events.AccessReviewDuePayload{
		TenantID:    tenantID.String(),
		KeyID:       v.Key.ID.String(),
		OwnerUserID: v.Key.OwnerUserID.String(),
		Name:        v.Key.Name,
		Reason:      v.Reason,
		DaysIdle:    daysIdle,
	}
	if err := w.pub.PublishAccessReviewDue(ctx, tenantID, payload); err != nil {
		w.logger.WarnContext(ctx, "access_review: publish failed", "key_id", v.Key.ID, "err", err)
	}
}

// accessReviewLockKey derives a stable int64 advisory-lock key from a
// tenant UUID. FNV-64a distributes UUIDs uniformly across the int8 key
// space Postgres requires (same technique as services/gc — see Decision
// #16 in CLAUDE.md §14).
//
// The "access-review:" salt keeps this lock namespace-separated from
// FUT-003's idle-revoke lock (salted with "idle-revoke:") so the two
// workers can sweep the same tenant in parallel.
func accessReviewLockKey(id uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("access-review:"))
	_, _ = h.Write(id[:])
	return int64(h.Sum64())
}

// RunningKeyLabel returns a debug label describing the current
// configuration — surfaced by callers that want a boot-time slog line
// so operators can spot "weekly cadence, N tenants configured" in logs
// without a separate metric.
func (w *AccessReview) RunningKeyLabel() string {
	return fmt.Sprintf("access_review tick=%s", w.tickPeriod)
}

// APIKeyTenantEnumerator wraps *repository.APIKeyRepository so the
// worker can list every tenant with at least one active API key
// without pulling the full APIKey rows. The bare method lives on this
// small wrapper because it's a worker-scoped query — nothing else in
// the codebase needs it today.
type APIKeyTenantEnumerator struct {
	pool *pgxpool.Pool
}

// NewAPIKeyTenantEnumerator constructs the wrapper. Caller passes the
// same pool used by APIKeyRepository so the query runs against the same
// connection settings.
func NewAPIKeyTenantEnumerator(pool *pgxpool.Pool) *APIKeyTenantEnumerator {
	return &APIKeyTenantEnumerator{pool: pool}
}

// ListTenantsWithActiveKeys returns every tenant_id that has at least
// one active row in api_keys. Tenants with only revoked keys are
// excluded — running the review for them is pure noise. Uses the same
// idx_api_keys_idle_check partial index the FUT-003 worker uses.
func (e *APIKeyTenantEnumerator) ListTenantsWithActiveKeys(ctx context.Context) ([]uuid.UUID, error) {
	const q = `SELECT DISTINCT tenant_id FROM api_keys WHERE is_active = true`
	rows, err := e.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list tenants with active keys: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Compile-time guarantee the wrapper implements the worker's contract.
var _ tenantEnumerator = (*APIKeyTenantEnumerator)(nil)
