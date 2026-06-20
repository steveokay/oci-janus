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

	"github.com/steveokay/oci-janus/services/gc/internal/collector"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// PersistedRunner reuses an existing collector but wraps every Run
// invocation in a persisted gc_runs row. The same instance services
// both the cron loop (CronTick) and the dispatcher (DispatchQueued).
type PersistedRunner struct {
	col  *collector.Collector
	repo *repository.Repository
	// mode tracks the collector's configured mode so cron-triggered
	// rows record the right gc_run_mode without leaking the choice into
	// the collector struct's public surface.
	mode string
}

// New builds a PersistedRunner. mode must match the collector's
// configured mode; it is used solely to populate gc_runs.mode on the
// cron-triggered sweeps.
func New(col *collector.Collector, repo *repository.Repository, mode string) *PersistedRunner {
	return &PersistedRunner{col: col, repo: repo, mode: mode}
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
func (r *PersistedRunner) runClaimed(ctx context.Context, runID uuid.UUID, mode string) {
	r.executeAndFinalize(ctx, runID, mode)
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
func (r *PersistedRunner) CronLoop(ctx context.Context, interval time.Duration, runRequests <-chan uuid.UUID) {
	// Initial sweep on startup matches the historical behaviour.
	r.CronTick(ctx)
	// And drain any queued rows that piled up while the service was
	// down — defensive, but cheap.
	for r.DispatchQueued(ctx) {
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
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
		case <-runRequests:
			// A new RunNow call arrived. The row is already queued in
			// the DB; just dispatch it (and any others waiting).
			for r.DispatchQueued(ctx) {
			}
		}
	}
}

// isNotFound is a local copy of the repository.ErrNotFound check to
// avoid an import cycle.
func isNotFound(err error) bool {
	return err == repository.ErrNotFound
}
