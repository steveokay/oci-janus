-- +goose Up

-- REDESIGN-001 Phase 5.1 / Review §A1 D1.
-- Replaces the (admin, org, '*') magic-string platform-admin convention with
-- a typed BOOLEAN column. Why: the legacy marker was a privilege-by-convention
-- — anyone who could call GrantRole with scope_value='*' minted a platform
-- admin, and the BFF authorization sites that needed to recognize the marker
-- had to special-case the literal string. Both are fragile; a typed column
-- removes both classes of bug at the type level.
--
-- Backfill: anyone currently holding (admin, org, '*') gets the flag.
-- After backfill, the legacy markers are DELETED so future code can't
-- accidentally rely on both representations.

ALTER TABLE users ADD COLUMN IF NOT EXISTS is_global_admin BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN users.is_global_admin IS
    'Platform admin — typed primitive replacing the (admin, org, ''*'') legacy marker. REDESIGN-001 Phase 5.1.';

-- Backfill: promote anyone holding the legacy marker grant.
UPDATE users u
SET is_global_admin = true
WHERE EXISTS (
    SELECT 1 FROM role_assignments ra
    JOIN roles r ON r.id = ra.role_id
    WHERE ra.user_id = u.id
      AND r.name = 'admin'
      AND ra.scope_type = 'org'
      AND ra.scope_value = '*'
);

-- Delete the legacy marker grants — they're redundant once is_global_admin is set.
-- This is INTENTIONALLY destructive: keeping both representations risks split-brain.
DELETE FROM role_assignments
WHERE scope_type = 'org'
  AND scope_value = '*'
  AND role_id = (SELECT id FROM roles WHERE name = 'admin');

-- +goose Down
-- Best-effort down: re-create the marker grants for users where is_global_admin=true.
-- Tenant assignment defaults to the user's own tenant_id since we lose tenant context
-- on the down path — operators rolling back must verify by hand.
INSERT INTO role_assignments (tenant_id, user_id, role_id, scope_type, scope_value, granted_by)
SELECT u.tenant_id, u.id, (SELECT id FROM roles WHERE name = 'admin'), 'org', '*', NULL
FROM users u
WHERE u.is_global_admin = true
ON CONFLICT DO NOTHING;

ALTER TABLE users DROP COLUMN IF EXISTS is_global_admin;
