-- +goose Up

-- FUT-012 Phase A — tenant-user lifecycle management.
--
-- Adds the columns that back the three new RPCs:
--
--   ListTenantUsers   needs `status` to render the status pill
--                     (active / invited / disabled).
--   InviteUser        needs `invite_token_hash` + `invite_expires_at`
--                     to persist the single-use invite token; the raw
--                     token is shown to the operator once at issue
--                     time and never read back from the DB.
--   SetUserDisabled   flips `status` between 'active' and 'disabled'
--                     while preserving 'invited' (a disable on a not-
--                     yet-accepted invite is a no-op).
--
-- `status` is denormalised from the existing `is_active` BOOLEAN +
-- pending invite state. We keep `is_active` for now (CLAUDE.md §11
-- "Never drop a column in a migration") — `status` becomes the new
-- source of truth at the application layer, both columns are written
-- in lockstep for one release, then a follow-up migration drops
-- `is_active`. This lets the auth-service rollout happen atomically
-- without a backwards-compat shim for ValidateToken / login readers.
--
-- Backfill rules:
--   is_active = TRUE  → status = 'active'
--   is_active = FALSE → status = 'disabled'
-- No row starts as 'invited' on the initial migration — invited is a
-- terminal state only the new InviteUser RPC can set. Service-account
-- shadow users (kind='service_account') always land as 'active' since
-- they have no human invite flow.
--
-- invite_token_hash stores the argon2id hash of a random token issued
-- at InviteUser time. The raw value never lands in the DB (same
-- discipline as users.password_hash + api_keys.key_hash). NULL when
-- status != 'invited' or when the invite has been accepted /
-- abandoned. invite_expires_at is the absolute wall-clock cutoff;
-- past it, the token is no longer redeemable regardless of hash.

ALTER TABLE users
    ADD COLUMN status              TEXT,
    ADD COLUMN invite_token_hash   TEXT,
    ADD COLUMN invite_expires_at   TIMESTAMPTZ;

UPDATE users
   SET status = CASE WHEN is_active THEN 'active' ELSE 'disabled' END;

ALTER TABLE users
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN status SET DEFAULT 'active',
    ADD CONSTRAINT users_status_check
        CHECK (status IN ('active', 'invited', 'disabled'));

-- Partial index on pending invites so the lookup path
-- (find invite by token hash, only consider non-expired rows) hits a
-- narrow index instead of scanning every user row.
CREATE INDEX users_pending_invite_idx
    ON users (invite_token_hash)
    WHERE status = 'invited' AND invite_token_hash IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS users_pending_invite_idx;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_status_check;

ALTER TABLE users
    DROP COLUMN IF EXISTS invite_expires_at,
    DROP COLUMN IF EXISTS invite_token_hash,
    DROP COLUMN IF EXISTS status;
