-- +goose Up

-- FUT-012 Phase A — tenant-user lifecycle management.
--
-- The original RBAC schema (20260614000001) restricted scope_type to
-- ('org', 'repo'). FUT-012 introduces a new scope: ('tenant'). A
-- tenant-admin holds (role=admin, scope_type='tenant', scope_value=<tenant_id>)
-- and can invite/list/disable users WITHIN their tenant. They do NOT
-- implicitly inherit admin on every org — that's a separate posture
-- (the "explicit elevate to org-admin per org" button on the FE,
-- shipping in Phase C).
--
-- The platform-admin marker (`(admin, org, '*')`) remains a separate,
-- higher-privilege scope and trumps tenant-admin everywhere.
--
-- No data backfill: there are no existing rows with scope_type='tenant'.
-- The CHECK constraint is dropped + recreated because Postgres won't
-- let us mutate it in place.

ALTER TABLE role_assignments
    DROP CONSTRAINT role_assignments_scope_type_check;

ALTER TABLE role_assignments
    ADD CONSTRAINT role_assignments_scope_type_check
    CHECK (scope_type IN ('org', 'repo', 'tenant'));

-- +goose Down

-- Down path purges any tenant-scope rows so the narrowed CHECK doesn't
-- fail on existing data. This is destructive but matches the "no
-- existing rows" invariant: a Down only fires in a rollback scenario
-- and we'd rather lose the tenant-admin grants than have a migration
-- that won't reverse.

DELETE FROM role_assignments WHERE scope_type = 'tenant';

ALTER TABLE role_assignments
    DROP CONSTRAINT role_assignments_scope_type_check;

ALTER TABLE role_assignments
    ADD CONSTRAINT role_assignments_scope_type_check
    CHECK (scope_type IN ('org', 'repo'));
