-- +goose Up

-- Deployment-scoped facts singleton table. One row per (key) across the
-- deployment's lifetime. Used by:
--   - bootstrap_tenant_id (REDESIGN-001 Phase 3.1, Q-003) — records the
--     UUID of the bootstrap tenant so the bootstrap CLI is idempotent.
--   - future deployment-scoped facts (KEK version prefixes for Phase 6.4,
--     schema baseline markers, etc).
--
-- Schema choice rationale:
--   - JSONB value column lets each key define its own shape without schema
--     churn. Bootstrap tenant id is a string UUID; KEK version is a struct
--     `{active: <hex>, previous: <hex>}`; etc.
--   - PRIMARY KEY on key gives O(1) lookups + natural uniqueness.
--   - updated_at lets us reason about when a key was last touched
--     (useful for audit + debugging).

CREATE TABLE deployment_metadata (
    key        TEXT        PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE deployment_metadata IS
    'Deployment-scoped facts (singleton per deployment). REDESIGN-001 Phase 3.1.a.';
COMMENT ON COLUMN deployment_metadata.key IS
    'Well-known key. Current keys: bootstrap_tenant_id (string UUID).';
COMMENT ON COLUMN deployment_metadata.value IS
    'JSONB value. Shape depends on key.';

-- +goose Down

DROP TABLE IF EXISTS deployment_metadata;
