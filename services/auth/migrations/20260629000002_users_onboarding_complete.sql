-- +goose Up

-- REDESIGN-001 Phase 4.3 §1.
-- Adds the per-user onboarding-complete flag consumed by the post-login
-- onboarding wizard. Why: the wizard is a one-time flow shown to genuinely-new
-- humans only; we need a typed, queryable signal that the user has dismissed
-- (or completed) it so we don't re-open the wizard on every session.
--
-- Backfill rules:
--   1. Anyone who existed BEFORE this migration ran was already using the
--      product without a wizard — sending them to it now would be jarring.
--      We mark them complete via the created_at < NOW() guard.
--   2. Service-account shadow users (kind='service_account') NEVER hit the
--      wizard — they're machine identities, not humans. Mark them complete
--      defensively so a future bug that tries to render the wizard for an
--      SA principal short-circuits instead of looping.
--
-- New users created after this migration default to false (NOT NULL DEFAULT
-- false) and stay there until POST /users/me/onboarding/complete flips the
-- flag.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS onboarding_complete BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN users.onboarding_complete IS
    'Per-user one-time wizard flag — true once the user has completed or dismissed the post-login onboarding wizard. REDESIGN-001 Phase 4.3.';

-- Backfill #1: pre-existing humans shouldn't be sent to the wizard.
-- created_at < NOW() is intentionally generous — every row that exists at
-- migration time was created before NOW(), so this catches the entire
-- existing population without needing a separate marker.
UPDATE users
SET    onboarding_complete = true
WHERE  created_at < NOW();

-- Backfill #2: service-account shadow users never onboard. Belt-and-braces
-- on top of the created_at backfill so any SA row that somehow lands with a
-- future created_at (clock skew, manual insert) is still marked complete.
UPDATE users
SET    onboarding_complete = true
WHERE  kind = 'service_account';

-- +goose Down

-- Drop the column. The wizard is purely a frontend concern; rolling back loses
-- the per-user completion state but the wizard itself can be hidden via the
-- frontend redesign rollback. No data archive needed.
ALTER TABLE users DROP COLUMN IF EXISTS onboarding_complete;
