-- +goose Up
-- +goose StatementBegin
--
-- FE-API-011/012/013: extend the users table for the dashboard's profile page.
--
-- * display_name — optional human-friendly name shown on /users/me. NULL when
--   the user hasn't set one (e.g. fresh signup, dev seed accounts). No length
--   constraint at the DB level — the handler enforces 1..128 chars.
-- * email — relax the NOT NULL constraint so accounts can be created without
--   an email (machine accounts, future SSO-managed users where email lives in
--   the IdP). We do NOT drop the existing UNIQUE (tenant_id, email) constraint:
--   the existing per-tenant uniqueness guarantee is still useful for accounts
--   that DO supply an email (multiple NULLs are allowed by Postgres so this
--   does not block null-email accounts). Whether to relax uniqueness further
--   is left as a follow-up — flagged in the FE-API-011/012/013 commit.
-- * last_login_at already exists nullable from 20260609000001_create_users.sql
--   (this migration only references it for completeness — no schema change).
--
-- CLAUDE.md §11 rule "never drop a column" applies to column removal; relaxing
-- NOT NULL is reversible (the down migration restores the constraint).

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS display_name TEXT NULL;

ALTER TABLE users
    ALTER COLUMN email DROP NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore NOT NULL on email. This will fail if any row has NULL email at
-- rollback time; that is intentional — the operator must decide how to
-- backfill before downgrading the schema.
ALTER TABLE users
    ALTER COLUMN email SET NOT NULL;

ALTER TABLE users
    DROP COLUMN IF EXISTS display_name;
-- +goose StatementEnd
