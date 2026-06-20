-- +goose Up

-- FE-API-032 — GC sweep persistence. One row per sweep attempt; rows
-- never delete so the dashboard can render the full run history.

-- gc_run_status mirrors the GCStatus.last_run_status enum exposed by the
-- gRPC layer. `queued` is the initial state when RunNow inserts a row
-- but the dispatcher hasn't picked it up yet.
CREATE TYPE gc_run_status AS ENUM ('queued','running','succeeded','failed');

-- gc_run_mode mirrors the legal values of services/gc's GC_MODE env var.
-- Keeping it as an enum (CHECK-equivalent) means an out-of-band SQL
-- INSERT can't bypass the BFF allowlist.
CREATE TYPE gc_run_mode AS ENUM ('dry-run','manifests','blobs','full');

CREATE TABLE gc_runs (
    run_id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- tenant_id is nullable because the cron-driven sweep iterates every
    -- tenant in a single pass — the row covers a cross-tenant run when
    -- NULL. Per-tenant invocations carry the target tenant_id so the
    -- audit log can attribute the work.
    tenant_id           UUID,
    mode                gc_run_mode NOT NULL,
    status              gc_run_status NOT NULL DEFAULT 'queued',
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    duration_ms         BIGINT,
    blobs_freed         BIGINT NOT NULL DEFAULT 0,
    manifests_deleted   BIGINT NOT NULL DEFAULT 0,
    bytes_freed         BIGINT NOT NULL DEFAULT 0,
    error_message       TEXT,
    -- triggered_by is `cron` for scheduled sweeps or the caller's
    -- user_id (UUID stringified) for manual RunNow invocations. Kept as
    -- TEXT (rather than nullable UUID) so the sentinel string and the
    -- audit lookup live on the same column.
    triggered_by        TEXT NOT NULL DEFAULT 'cron'
);

-- idx_gc_runs_completed supports GetStatus + ListRuns ordering. NULLS
-- LAST keeps in-flight runs (no completed_at) at the top of the list so
-- the dashboard sees the active sweep before the historical ones.
CREATE INDEX idx_gc_runs_completed
    ON gc_runs(completed_at DESC NULLS LAST);

-- idx_gc_runs_status supports the dispatcher's `pick the next queued
-- row` lookup and the BFF's per-status filter (future enhancement).
CREATE INDEX idx_gc_runs_status
    ON gc_runs(status, requested_at);

-- +goose Down

DROP TABLE IF EXISTS gc_runs;
DROP TYPE  IF EXISTS gc_run_status;
DROP TYPE  IF EXISTS gc_run_mode;
