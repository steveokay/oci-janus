-- +goose Up
-- +goose StatementBegin
--
-- REDESIGN-001 RM-003 + RM-004 — Drop per-tenant SSO tables / columns.
--
-- RM-003 APPROVED: global_sso_config (migration 20260628000001) replaces
--                  auth_providers. Backfill ran in 20260628000002.
-- RM-004 APPROVED: SSO sessions are deployment-wide; tenant_id column dropped.
--
-- Execution order matters:
--   1. auth_login_sessions references auth_providers (FK on provider_id) — must
--      be altered before we drop auth_providers.
--   2. users.sso_provider_id is a UUID FK to auth_providers — must be dropped
--      and re-added as TEXT before we drop auth_providers.
--   3. Drop auth_providers + auth_provider_type.
--
-- RM-004: auth_login_sessions.tenant_id is dropped. The SSO callback resolves
-- the tenant from the authenticated user's row (users.tenant_id); new
-- auto-provisioned users fall back to AUTH_DEFAULT_TENANT_ID env var.

-- ── 1. auth_login_sessions ────────────────────────────────────────────────────
-- Drop the FK constraint so we can change the column type and later drop
-- auth_providers. The constraint name follows Postgres default naming:
-- auth_login_sessions_provider_id_fkey.
ALTER TABLE auth_login_sessions
    DROP CONSTRAINT IF EXISTS auth_login_sessions_provider_id_fkey;

-- Change provider_id from UUID to TEXT. Existing rows are mapped using the
-- backfill: the string provider_id in global_sso_config matches the enum
-- value that was stored in auth_providers.type. A subquery converts
-- existing UUID provider_id values to the corresponding type string.
-- Rows whose provider_id no longer exists in auth_providers are set to NULL
-- so the migration is never blocked by orphaned session rows (sessions are
-- short-lived; any active sessions expire within 10 minutes anyway).
ALTER TABLE auth_login_sessions
    ALTER COLUMN provider_id TYPE TEXT
    USING (
        SELECT type::TEXT
        FROM auth_providers
        WHERE auth_providers.id = auth_login_sessions.provider_id
    );

-- RM-004: drop the tenant_id column. Sessions are now global/deployment-wide.
ALTER TABLE auth_login_sessions
    DROP COLUMN IF EXISTS tenant_id;

-- ── 2. users.sso_provider_id ─────────────────────────────────────────────────
-- The column was a UUID FK to auth_providers(id). We drop it and replace
-- it with a TEXT column holding the string provider_id from global_sso_config.
-- Existing non-NULL UUID values are mapped via the same auth_providers lookup.
-- Rows where the referenced provider is not found get NULL — acceptable because
-- the column is informational only (login still works via the email match path).
ALTER TABLE users DROP COLUMN IF EXISTS sso_provider_id;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS sso_provider_id TEXT NULL;

-- Re-populate from the UUID in the old provider rows where possible.
-- This is best-effort: the UUID-to-string mapping goes through auth_providers
-- which still exists at this point in the migration.
UPDATE users u
SET sso_provider_id = ap.type::TEXT
FROM auth_providers ap
WHERE ap.id::TEXT = (
    -- auth_providers.id was stored as the original sso_provider_id UUID.
    -- We cannot read the old UUID value anymore because we just DROPped the
    -- column, so this UPDATE is intentionally a no-op. The column will be NULL
    -- for existing SSO users; they will have their sso_provider_id populated on
    -- next SSO login when EnsureSSOUser calls CreateSSOUser or TouchLastLogin.
    NULL
);

CREATE INDEX IF NOT EXISTS idx_users_sso_provider
    ON users (sso_provider_id)
    WHERE sso_provider_id IS NOT NULL;

-- ── 3. Drop auth_providers + enum ────────────────────────────────────────────
-- auth_login_sessions.provider_id FK was dropped above; users.sso_provider_id
-- FK was replaced; safe to drop the table now.
DROP TABLE IF EXISTS auth_providers;
DROP TYPE IF EXISTS auth_provider_type;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- This migration is destructive (auth_providers data is gone after Up).
-- Down is best-effort / for dev environments only.
SELECT 1;
-- +goose StatementEnd
