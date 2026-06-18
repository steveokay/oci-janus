-- +goose Up
-- +goose StatementBegin
--
-- Follow-up to 20260618000001_seed_dev_admin_role.sql.
--
-- Why this is a separate migration rather than a re-edit of -001:
--   goose tracks applied migrations by version number, not file content.
--   The first version of -001 only inserted (admin / org / dev). Later we
--   amended it to also insert the platform-admin marker (admin / org / *),
--   but any dev DB that had already run the first version did not pick up
--   the second INSERT — goose saw the version as applied and skipped it.
--
--   On a fresh `make up` this migration is a no-op because the marker row
--   already exists from -001 (ON CONFLICT DO NOTHING). On an upgraded dev
--   DB it backfills the missing row so the super-admin GUI works without
--   manual psql.
--
-- The literal "*" scope_value is reserved for the platform-admin marker
-- because validateOrgName rejects "*" — it cannot collide with a real org
-- name (PENTEST-024).

INSERT INTO role_assignments (id, tenant_id, user_id, role_id, scope_type, scope_value, granted_by)
SELECT
    '00000000-0000-0000-0000-000000000021'::uuid,
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e'::uuid,
    '00000000-0000-0000-0000-000000000002'::uuid,
    id,
    'org',
    '*',
    NULL
FROM roles
WHERE name = 'admin'
ON CONFLICT (user_id, role_id, scope_type, scope_value) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Mirror the row inserted above. -001's Down already targets the same id
-- (00000000-0000-0000-0000-000000000021), so rolling back both -001 and
-- -002 still leaves the DB consistent — the second DELETE is a no-op.
DELETE FROM role_assignments
WHERE id = '00000000-0000-0000-0000-000000000021';
-- +goose StatementEnd
