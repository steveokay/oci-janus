-- REM-013 gap 3: partial index backing GetTenantRetentionSavings.
--
-- The dashboard storage-breakdown card calls the savings aggregate
-- (SUM(bytes_freed) ... WHERE tenant_id = $1 AND mode IN
-- ('retention','retention_grace') AND status = 'succeeded') on every page
-- load. Neither existing index covers tenant_id, so the query planner would
-- fall back to a sequential scan as gc_runs history accumulates. The partial
-- predicate mirrors the query exactly, so the index stays tiny (succeeded
-- retention rows only) while making the aggregate an index-only-ish lookup.

-- +goose Up
CREATE INDEX idx_gc_runs_retention_savings
    ON gc_runs(tenant_id)
    WHERE mode IN ('retention', 'retention_grace') AND status = 'succeeded';

-- +goose Down
DROP INDEX IF EXISTS idx_gc_runs_retention_savings;
