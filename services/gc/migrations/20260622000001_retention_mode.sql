-- +goose NO TRANSACTION
-- FE-API-040: extend gc_run_mode with the two retention modes so the existing
-- gc_runs table can record retention sweeps alongside the dry-run / manifests
-- / blobs / full sweeps from FE-API-032.
--
-- `retention` is the soft-delete pass — evaluates the effective policy for
-- a repo and stamps retention_pending_delete_at on matched manifests via
-- metadata.MarkManifestRetentionPending.
--
-- `retention_grace` is the finaliser — deletes manifests whose pending stamp
-- is older than the configured grace window (default 7 days). Dispatched
-- either by the cross-tenant grace ticker (every RETENTION_GRACE_INTERVAL_HOURS,
-- default 6h) or by an explicit operator trigger.
--
-- NO TRANSACTION is required because ALTER TYPE ... ADD VALUE cannot run
-- inside a transaction in Postgres. IF NOT EXISTS keeps Up idempotent across
-- a goose-redown / goose-up cycle (the values stick around even if Down ran
-- — see below).

-- +goose Up
ALTER TYPE gc_run_mode ADD VALUE IF NOT EXISTS 'retention';
ALTER TYPE gc_run_mode ADD VALUE IF NOT EXISTS 'retention_grace';

-- repo_id is populated for retention modes (which are always repo-scoped) and
-- left NULL for the existing dry-run / manifests / blobs / full sweeps. Plain
-- UUID column with no FK — the gc service does not have visibility into the
-- metadata schema's repositories table, so we keep the value as opaque
-- correlation data the BFF can render alongside the run row.
ALTER TABLE gc_runs ADD COLUMN IF NOT EXISTS repo_id UUID;

-- +goose Down
-- Postgres has no clean removal path for enum values short of rewriting every
-- column referencing the enum, so the Down is intentionally a no-op. Leaving
-- the values in place is safe — a redown ⇒ reup cycle still leaves the type
-- exactly where Up wanted it. The only consequence is a slightly fuller enum.
SELECT 1;
