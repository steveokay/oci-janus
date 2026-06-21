-- +goose Up

-- FE-API-042: manifests.last_pulled_at supports pull-activity tracking and
-- the FE-API-043 max_idle_days retention rule. Updated by the metadata
-- pull.image consumer with a 24h debounce so hot manifests pulled many
-- times per day generate at most one Postgres write per (manifest, day).
--
-- NULL is meaningful: it means "never pulled since the column was added"
-- and the FE-API-043 max_idle_days evaluator treats NULL AS IDLE — i.e.
-- a manifest with no recorded pull counts toward the idle threshold.
-- This ticket deliberately does NOT backfill existing rows.
ALTER TABLE manifests
  ADD COLUMN last_pulled_at TIMESTAMPTZ NULL;

-- Composite index for the FE-API-043 retention max_idle_days lookup:
--   SELECT ... FROM manifests
--    WHERE repo_id = $1
--      AND (last_pulled_at IS NULL OR last_pulled_at < NOW() - INTERVAL '...')
--
-- NULLS FIRST is intentional — never-pulled manifests are the most likely
-- candidates for max_idle_days deletion, so we want them at the front of
-- the index scan.
CREATE INDEX idx_manifests_last_pulled
  ON manifests(repo_id, last_pulled_at NULLS FIRST);

-- +goose Down
DROP INDEX IF EXISTS idx_manifests_last_pulled;
ALTER TABLE manifests DROP COLUMN IF EXISTS last_pulled_at;
