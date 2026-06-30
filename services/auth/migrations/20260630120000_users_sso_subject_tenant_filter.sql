-- +goose Up
-- +goose StatementBegin

-- SEC-040 — multi-tenant boundary on SSO subject lookup.
--
-- The Phase 5.5 partial index `idx_users_sso_subject` keyed on
-- (sso_provider_id, sso_subject) was global, not tenant-scoped. In
-- DEPLOYMENT_MODE=multi a recycled IdP subject reachable from two tenants
-- sharing one provider (e.g. both using Google Workspace OAuth) could
-- resolve to a user in the wrong tenant. Single-tenant deployments were
-- safe in practice (only one tenant exists) but the schema-level guard
-- belongs here so the gap cannot reopen when DEPLOYMENT_MODE=multi
-- becomes the active posture.
--
-- The new index leads with tenant_id so the (tenant_id, provider_id,
-- subject) tuple is the lookup key. The previous index is dropped — keep
-- the partial WHERE clause so pre-migration NULL-subject rows stay
-- excluded until they get backfilled on first login.

DROP INDEX IF EXISTS idx_users_sso_subject;

CREATE INDEX IF NOT EXISTS idx_users_sso_subject_tenant
    ON users (tenant_id, sso_provider_id, sso_subject)
    WHERE sso_subject IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_sso_subject_tenant;

CREATE INDEX IF NOT EXISTS idx_users_sso_subject
    ON users (sso_provider_id, sso_subject)
    WHERE sso_subject IS NOT NULL;

-- +goose StatementEnd
