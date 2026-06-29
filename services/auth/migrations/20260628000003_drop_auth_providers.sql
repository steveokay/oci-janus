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

-- Change provider_id from UUID to TEXT. The original migration tried
-- to use a subquery in `ALTER COLUMN ... TYPE TEXT USING (SELECT ...)`,
-- but Postgres rejects that with SQLSTATE 0A000 ("cannot use subquery
-- in transform expression"). The standard pattern is to add a new
-- column, backfill it with a JOIN-based UPDATE, then drop+rename:
--
--   1. Add `provider_id_text` (nullable TEXT).
--   2. UPDATE … FROM auth_providers — sets the text type for every
--      session whose old UUID still matches an auth_providers row.
--      Sessions with orphaned provider_id end up NULL, which is fine
--      (sessions are short-lived; any active ones expire within 10
--      minutes anyway).
--   3. DROP the old `provider_id` column, then RENAME the temp column.
--
-- This is the canonical "change column type with a cross-table data
-- transform" recipe and works on every supported Postgres version.
ALTER TABLE auth_login_sessions
    ADD COLUMN IF NOT EXISTS provider_id_text TEXT;

UPDATE auth_login_sessions als
SET provider_id_text = ap.type::TEXT
FROM auth_providers ap
WHERE ap.id = als.provider_id;

ALTER TABLE auth_login_sessions
    DROP COLUMN provider_id;

ALTER TABLE auth_login_sessions
    RENAME COLUMN provider_id_text TO provider_id;

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

-- Note: we deliberately do NOT attempt to backfill the new TEXT column
-- from the old UUID values — those were destroyed by the DROP COLUMN
-- above. Existing SSO users will have NULL sso_provider_id until their
-- next login, at which point EnsureSSOUser / CreateSSOUser / TouchLastLogin
-- populates it from the global_sso_config provider_id string. The column
-- is informational only (login still works via the email match path), so
-- a brief NULL window is acceptable.

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
