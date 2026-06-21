-- +goose Up
-- FE-API-040: retention executor soft-delete marker.
--
-- The retention sweep marks manifests for deletion by stamping
-- retention_pending_delete_at = NOW. A separate grace sweep (cron-driven,
-- every 6h by default) hard-deletes them once NOW - retention_pending_delete_at
-- exceeds the configured grace window (default 7 days).
--
-- Soft-delete + grace window are deliberate: an accidental policy that selects
-- "the entire repo" surfaces in the dashboard for 7 days before any data is
-- actually gone. The UI can offer an "undo" affordance during that window via
-- the ClearManifestRetentionPending RPC.
--
-- Future work — NOT this ticket:
--   - tag_removed_at column (FE-API would need its own ticket): would let
--     dangling_grace_days be authoritative rather than the
--     "no tags + created_at" approximation FE-API-038's evaluator uses.

ALTER TABLE manifests
  ADD COLUMN retention_pending_delete_at TIMESTAMPTZ NULL;

-- Partial index — only the rows that need a deadline check appear here, so
-- the grace sweep's WHERE retention_pending_delete_at IS NOT NULL AND
-- retention_pending_delete_at < NOW() - INTERVAL '<grace_days> days' becomes
-- an index-only seek even at high manifest counts.
CREATE INDEX idx_manifests_retention_pending
  ON manifests(retention_pending_delete_at)
  WHERE retention_pending_delete_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_manifests_retention_pending;
ALTER TABLE manifests DROP COLUMN IF EXISTS retention_pending_delete_at;
