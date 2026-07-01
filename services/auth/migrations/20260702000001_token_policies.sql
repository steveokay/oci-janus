-- +goose Up
-- +goose StatementBegin

-- FUT-003 — workspace-wide token policy. One row per tenant.
-- All limit columns are NULL by default = policy disabled for that
-- dimension. This lets a workspace admin opt in to individual limits
-- without configuring every dimension at once.
CREATE TABLE token_policies (
    tenant_id              UUID        PRIMARY KEY,
    max_ttl_days           INTEGER,             -- NULL = no cap
    rotation_interval_days INTEGER,             -- NULL = no force-rotation
    idle_revoke_days       INTEGER,             -- NULL = no idle-revoke
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by_user_id     UUID
);

-- api_keys.rotation_due_at — set on CreateAPIKey when the workspace
-- policy has rotation_interval_days configured. Consumed by FUT-004's
-- rotation-lapse surface. NULL means "no rotation deadline"; not-null
-- and past-now means "overdue for rotation".
--
-- Note: api_keys.last_used_at already exists from the initial
-- 20260609000002_create_api_keys.sql migration — no need to re-add.
ALTER TABLE api_keys ADD COLUMN rotation_due_at TIMESTAMPTZ;

-- api_keys.revoke_reason — recorded on revocation so an operator can
-- distinguish manual admin revocation from the idle-revoke worker's
-- automated action. NULL for keys that are still active OR were revoked
-- pre-migration (grandfathered — the column is best-effort).
--
-- Vocabulary: "manual" | "idle_revoked" | "rotation_lapsed" (the last is
-- reserved for FUT-004; FUT-003 only writes the first two).
ALTER TABLE api_keys ADD COLUMN revoke_reason TEXT;

-- Partial index for the idle-revoke worker's per-tenant scan. Filtering
-- on `is_active = true` keeps the index small — revoked keys are dead
-- weight for the worker's query and would inflate the index needlessly.
--
-- We index (tenant_id, last_used_at) so the worker's ORDER BY-less
-- per-tenant scan can walk the index directly without a table lookup
-- on the hot path.
CREATE INDEX idx_api_keys_idle_check
    ON api_keys (tenant_id, last_used_at)
    WHERE is_active = true;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_api_keys_idle_check;
ALTER TABLE api_keys DROP COLUMN IF EXISTS revoke_reason;
ALTER TABLE api_keys DROP COLUMN IF EXISTS rotation_due_at;
DROP TABLE IF EXISTS token_policies;

-- +goose StatementEnd
