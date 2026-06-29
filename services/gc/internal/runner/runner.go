// Package runner wraps the GC collector with persistence + a dispatch
// channel so the cron loop (scheduled sweeps) and the gRPC RunNow RPC
// (ad-hoc sweeps) both flow through the same code path.
//
// FE-API-032 introduced this package. Before it the cron loop in
// services/gc/internal/server invoked collector.Collector.Run directly
// and only logged the result. Now every sweep — whether triggered by
// the cron tick or by a queued gc_runs row — is recorded as a row
// transition (queued → running → succeeded/failed) so the dashboard
// can render a complete history.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/gc/internal/collector"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// EventPublisher is the narrow surface the retention executor needs on the
// libs/rabbitmq/publisher.Publisher. Decoupling lets the executor accept a
// nil publisher (no broker wired) and lets tests drop in a fake that
// captures the routing key + event body without standing up RabbitMQ.
//
// Matches *publisher.Publisher.Publish exactly so the production wire is a
// one-liner in server.go.
type EventPublisher interface {
	Publish(ctx context.Context, routingKey string, event events.Event) error
}

// PersistedRunner reuses an existing collector but wraps every Run
// invocation in a persisted gc_runs row. The same instance services
// both the cron loop (CronTick) and the dispatcher (DispatchQueued).
//
// FE-API-040 added the retention executor surface (RunRetention /
// RunRetentionGrace) onto the same struct so the cron loop can dispatch
// both legacy and retention modes through one channel.
type PersistedRunner struct {
	col  *collector.Collector
	repo *repository.Repository
	// mode tracks the collector's configured mode so cron-triggered
	// rows record the right gc_run_mode without leaking the choice into
	// the collector struct's public surface.
	mode string
	// metaClient is the retention executor's only collaborator. Nil disables
	// the retention dispatch branches — the dispatcher logs and skips the
	// row, marking it failed so it doesn't stay queued forever.
	metaClient MetadataClient
	// retention bundles the knobs the executor uses (grace window, per-run
	// caps). Defaults are wired in New; SetRetentionConfig swaps them in
	// before CronLoop spawns.
	retention RetentionConfig
	// pub publishes retention.* events. Nil-safe: when unset (no broker
	// configured) the executor logs a debug and skips the publish so a
	// dev install without RABBITMQ_URL still drains queued retention rows.
	pub EventPublisher
	// finalizeHook / failHook are TEST-ONLY indirections so the retention
	// executor's outcome-recording can be observed without standing up a
	// real repository.Repository. In production both are nil and the
	// executor calls r.repo.FinalizeRetentionRun / FailRun directly.
	finalizeHook func(ctx context.Context, runID uuid.UUID, count, blobs, bytes int64, errMsg string) error
	failHook     func(ctx context.Context, runID uuid.UUID, msg string) error
}

// finalize is the indirection layer the retention executor uses so tests can
// capture FinalizeRetentionRun without a real pool. Production callers
// (nil hook) fall through to the repository.
//
// the future "GC swept N blobs as part of retention" rollup; keeping the
// param means callers don't change when that lands.
//
//nolint:unparam // `blobs` is always 0 today but the schema reserves it for
func (p *PersistedRunner) finalize(ctx context.Context, runID uuid.UUID, count, blobs, bytes int64, errMsg string) error {
	if p.finalizeHook != nil {
		return p.finalizeHook(ctx, runID, count, blobs, bytes, errMsg)
	}
	return p.repo.FinalizeRetentionRun(ctx, runID, count, blobs, bytes, errMsg)
}

// fail is the same indirection for FailRun.
func (p *PersistedRunner) fail(ctx context.Context, runID uuid.UUID, msg string) error {
	if p.failHook != nil {
		return p.failHook(ctx, runID, msg)
	}
	return p.repo.FailRun(ctx, runID, msg)
}

// New builds a PersistedRunner. mode must match the collector's
// configured mode; it is used solely to populate gc_runs.mode on the
// cron-triggered sweeps.
//
// metaClient may be nil — the retention dispatch branches surface a
// FailedPrecondition-equivalent on the run row when it is, so a deployment
// with the legacy collector configured but no retention wiring still
// boots cleanly.
func New(col *collector.Collector, repo *repository.Repository, mode string) *PersistedRunner {
	return &PersistedRunner{
		col:       col,
		repo:      repo,
		mode:      mode,
		retention: defaultRetentionConfig(),
	}
}

// WithMetadataClient attaches the retention executor's metadata RPC stub.
// Returns the runner for chained init.
func (p *PersistedRunner) WithMetadataClient(c MetadataClient) *PersistedRunner {
	p.metaClient = c
	return p
}

// WithPublisher attaches a RabbitMQ event publisher to the retention
// executor. Pass nil (or skip the call) to disable retention.* event
// emission — useful for dev installs without a broker, and required by
// the legacy non-persisted path. Returns the runner for chained init.
func (p *PersistedRunner) WithPublisher(pub EventPublisher) *PersistedRunner {
	p.pub = pub
	return p
}

// CronTick records a cron-triggered sweep. INSERTs a row with
// triggered_by='cron', flips it to `running`, runs the collector, and
// finalises the row. Returns an error only if persistence fails — a
// collector error is recorded on the row and slog-logged, but the
// caller (the cron loop) still gets nil so the schedule doesn't stall.
func (r *PersistedRunner) CronTick(ctx context.Context) {
	r.run(ctx, "cron-tick", func() (uuid.UUID, error) {
		// CreateRun starts in `queued` then we immediately flip it to
		// `running`; this gives ListRuns a brief queued window that
		// matches the manual flow's lifecycle.
		rec, err := r.repo.CreateRun(ctx, r.mode, uuid.Nil, "cron")
		if err != nil {
			return uuid.Nil, fmt.Errorf("create cron run row: %w", err)
		}
		return rec.RunID, nil
	}, r.mode)
}

// DispatchQueued claims a queued row (typically just inserted by
// RunNow) and executes the sweep against the collector. Multiple
// gc replicas race on `FOR UPDATE SKIP LOCKED` so only one of them
// picks up each row.
//
// Returns true when a row was processed, false when no queued row
// was waiting — callers can use that as a signal to skip the next
// tick.
func (r *PersistedRunner) DispatchQueued(ctx context.Context) bool {
	rec, err := r.repo.ClaimNextQueued(ctx)
	if err != nil {
		if isNotFound(err) {
			return false
		}
		slog.ErrorContext(ctx, "gc dispatch: claim queued failed", "error", err)
		return false
	}
	r.runClaimed(ctx, rec.RunID, rec.Mode)
	return true
}

// run is the shared wrapper for CronTick. The createRow callback
// performs the initial INSERT (cron flow) — the dispatch flow uses
// runClaimed since the row is already `running` after ClaimNextQueued.
func (r *PersistedRunner) run(ctx context.Context, label string, createRow func() (uuid.UUID, error), mode string) {
	runID, err := createRow()
	if err != nil {
		slog.ErrorContext(ctx, "gc "+label+": create row failed", "error", err)
		return
	}
	// Flip queued → running before we kick off the sweep so a sibling
	// dispatcher can never re-claim the same row.
	if err := r.repo.StartRun(ctx, runID); err != nil {
		slog.ErrorContext(ctx, "gc "+label+": start row failed", "error", err, "run_id", runID)
		// Try to mark the row as failed to avoid leaving it queued
		// forever; ignore secondary errors.
		_ = r.repo.FailRun(ctx, runID, fmt.Sprintf("start failed: %v", err))
		return
	}
	r.executeAndFinalize(ctx, runID, mode)
}

// runClaimed is the dispatch-flow equivalent of run: the row is
// already running (ClaimNextQueued set started_at + status), so we
// jump straight to the execute step.
//
// FE-API-040: when mode is one of the retention modes we hand off to the
// retention executor rather than the legacy collector. The full row is
// fetched so the executor has the run's repo_id (which the legacy collector
// doesn't need).
func (r *PersistedRunner) runClaimed(ctx context.Context, runID uuid.UUID, mode string) {
	if IsRetentionMode(mode) {
		r.dispatchRetention(ctx, runID, mode)
		return
	}
	r.executeAndFinalize(ctx, runID, mode)
}

// dispatchRetention hydrates the full gc_runs row and routes to the matching
// retention executor. Errors are recorded on the row so the dashboard sees
// the failure rather than a forever-running row.
func (r *PersistedRunner) dispatchRetention(ctx context.Context, runID uuid.UUID, mode string) {
	if r.metaClient == nil {
		_ = r.repo.FailRun(ctx, runID, "retention executor not configured (metadata client missing)")
		return
	}
	run, err := r.repo.GetRunByID(ctx, runID, uuid.Nil)
	if err != nil {
		slog.ErrorContext(ctx, "retention dispatch: get run failed", "error", err, "run_id", runID)
		_ = r.repo.FailRun(ctx, runID, fmt.Sprintf("get run: %v", err))
		return
	}
	var execErr error
	switch mode {
	case "retention":
		execErr = r.RunRetention(ctx, run)
	case "retention_grace":
		execErr = r.RunRetentionGrace(ctx, run)
	default:
		execErr = fmt.Errorf("unknown retention mode %q", mode)
	}
	if execErr != nil {
		// RunRetention / RunRetentionGrace already finalise on the happy
		// path; an error returned here means the finalise itself failed
		// (e.g. row vanished). Surface it so the next operator who polls
		// GetStatus sees what happened.
		slog.WarnContext(ctx, "retention dispatch: executor returned error",
			"run_id", runID, "mode", mode, "error", execErr)
		_ = r.repo.FailRun(ctx, runID, execErr.Error())
	}
}

// executeAndFinalize runs the collector and updates the gc_runs row
// with the result. A panicking sweep is caught here so the row never
// stays `running` forever.
func (r *PersistedRunner) executeAndFinalize(ctx context.Context, runID uuid.UUID, mode string) {
	// Defer guarantees we either succeed/fail the row even if the
	// collector panics deep in the sweep.
	var (
		res    *collector.Result
		runErr error
	)
	func() {
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("gc panic: %v", p)
			}
		}()
		res, runErr = r.col.Run(ctx)
	}()

	if runErr != nil {
		// Best-effort failure recording. We don't surface the error
		// further; the caller already moved on (cron) or doesn't care
		// (dispatch).
		if err := r.repo.FailRun(ctx, runID, runErr.Error()); err != nil {
			slog.ErrorContext(ctx, "gc finalize: fail row failed",
				"error", err, "run_id", runID, "sweep_error", runErr)
		}
		slog.WarnContext(ctx, "gc sweep failed",
			"run_id", runID, "mode", mode, "error", runErr)
		return
	}

	var blobs, manifests, bytes int64
	if res != nil {
		blobs = int64(res.BlobsDeleted)
		manifests = int64(res.ManifestsDeleted)
		bytes = res.BytesFreed
	}
	if err := r.repo.CompleteRun(ctx, runID, blobs, manifests, bytes); err != nil {
		slog.ErrorContext(ctx, "gc finalize: complete row failed",
			"error", err, "run_id", runID)
	}
	slog.InfoContext(ctx, "gc sweep finished",
		"run_id", runID, "mode", mode,
		"manifests_deleted", manifests, "blobs_freed", blobs, "bytes_freed", bytes)
}

// CronLoop blocks running cron-driven sweeps every `interval` until
// ctx is cancelled. Between ticks it also drains any queued rows
// from RunNow so manual sweeps don't have to wait for the next tick.
//
// runRequests carries hints from the gRPC RunNow handler — they're
// not authoritative (the persisted row is) but they let the loop
// react immediately rather than polling for queued work every tick.
//
// FE-API-040: a second ticker fires every `graceInterval` and enqueues a
// cross-tenant retention_grace row. When graceInterval is zero the grace
// ticker is disabled (used for tests that drive the dispatcher manually).
func (r *PersistedRunner) CronLoop(ctx context.Context, interval, graceInterval time.Duration, runRequests <-chan uuid.UUID) {
	// Initial sweep on startup matches the historical behaviour.
	r.CronTick(ctx)
	// And drain any queued rows that piled up while the service was
	// down — defensive, but cheap.
	for r.DispatchQueued(ctx) {
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// graceTicker is optional — only when the retention executor is wired and
	// the operator configured a non-zero interval. Using a long-period
	// fallback ticker when disabled keeps the select statement uniform; the
	// case is dropped because graceCh stays nil.
	var graceCh <-chan time.Time
	if r.metaClient != nil && graceInterval > 0 {
		gt := time.NewTicker(graceInterval)
		defer gt.Stop()
		graceCh = gt.C
		// Kick off one grace sweep on startup so a stale install doesn't
		// have to wait `graceInterval` for the finaliser to drain anything
		// that's already past the window.
		r.enqueueGraceSweep(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.CronTick(ctx)
			// Drain queued rows after each cron sweep so the manual
			// queue can never starve.
			for r.DispatchQueued(ctx) {
			}
		case <-graceCh:
			// Retention grace ticker — enqueue one cross-tenant
			// retention_grace row and let the dispatcher pick it up.
			r.enqueueGraceSweep(ctx)
			for r.DispatchQueued(ctx) {
			}
		case <-runRequests:
			// A new RunNow / TriggerRetentionRun call arrived. The row
			// is already queued in the DB; just dispatch it (and any
			// others waiting).
			for r.DispatchQueued(ctx) {
			}
		}
	}
}

// enqueueGraceSweep inserts a cross-tenant retention_grace row that the
// dispatcher will claim on the next pass. Cross-tenant (tenant_id=nil) so
// the metadata-side ListPendingDeleteManifests scans every tenant in one
// pass. Errors are logged but do not abort the ticker.
func (r *PersistedRunner) enqueueGraceSweep(ctx context.Context) {
	if r.metaClient == nil {
		return
	}
	rec, err := r.repo.CreateRun(ctx, "retention_grace", uuid.Nil, "cron-grace")
	if err != nil {
		slog.WarnContext(ctx, "retention_grace: enqueue failed", "error", err)
		return
	}
	slog.InfoContext(ctx, "retention_grace: enqueued cron sweep", "run_id", rec.RunID)
}

// isNotFound is a local copy of the repository.ErrNotFound check to
// avoid an import cycle.
func isNotFound(err error) bool {
	return err == repository.ErrNotFound
}
