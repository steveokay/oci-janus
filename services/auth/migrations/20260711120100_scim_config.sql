-- +goose Up
-- Single global SCIM provisioning config (D2). The CHECK (id = 1) + fixed default
-- enforces a singleton row, mirroring the deployment-wide global_sso_config posture.
-- No runtime-role GRANT block: registry-auth migrations are owner-only (no low-
-- privilege runtime role like registry_audit_app exists for the auth database), so
-- the pool owner already holds SELECT/INSERT/UPDATE on this table.
CREATE TABLE scim_config (
    id           SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1), -- singleton row
    tenant_id    UUID NOT NULL,
    token_hash   TEXT,                     -- Argon2id PHC string; NULL = disabled
    enabled      BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS scim_config;
