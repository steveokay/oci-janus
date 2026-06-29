-- +goose Up
-- +goose StatementBegin

-- REDESIGN-001 Phase 5.5 — SSO subject-id binding.
--
-- Match SSO logins on (sso_provider_id, sso_subject) instead of email alone so
-- a recycled corporate email cannot accidentally take over a prior employee's
-- account. The IdP's stable subject identifier (OAuth `sub` claim / SAML
-- NameID) is the only identifier guaranteed to remain bound to a single
-- account for the lifetime of that account at the IdP.
--
-- Backfill strategy: leave existing rows with sso_subject NULL. EnsureSSOUser
-- detects NULL on a successful email+provider match and writes the subject in
-- on first login, so the population is filled gradually without a downtime
-- window. The partial index keeps the lookup fast once subjects are present.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS sso_subject TEXT;

COMMENT ON COLUMN users.sso_subject IS
    'IdP-assigned stable subject identifier — OAuth `sub` claim or SAML NameID. REDESIGN-001 Phase 5.5; used together with sso_provider_id to defend against email-recycle account takeover.';

-- Composite lookup index for the (sso_provider_id, sso_subject) match path.
-- Partial WHERE clause keeps the index small while the backfill catches up —
-- pre-migration users have NULL subjects and are excluded until first login.
CREATE INDEX IF NOT EXISTS idx_users_sso_subject
    ON users (sso_provider_id, sso_subject)
    WHERE sso_subject IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_sso_subject;
ALTER TABLE users DROP COLUMN IF EXISTS sso_subject;

-- +goose StatementEnd
