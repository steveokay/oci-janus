-- +goose Up
-- FE-API-015: surface the scan trigger source on scan history rows.
-- Existing rows are all push-triggered (push.completed is the only path that
-- currently creates scan_results), so backfill default 'push'. Future code
-- paths (manual via /api/v1/.../scan POST or a scheduled cron job) should
-- write 'manual' or 'scheduled' explicitly; the column is NOT NULL with a
-- CHECK constraint to keep the enum honest at write time.
ALTER TABLE scan_results
    ADD COLUMN trigger TEXT NOT NULL DEFAULT 'push'
        CHECK (trigger IN ('push','manual','scheduled'));

-- Composite index supports the FE-API-015 keyset cursor
-- (ORDER BY completed_at DESC, id DESC) without a full table scan.
CREATE INDEX idx_scan_results_tenant_completed_at
    ON scan_results(tenant_id, completed_at DESC NULLS LAST, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_scan_results_tenant_completed_at;
ALTER TABLE scan_results DROP COLUMN IF EXISTS trigger;
