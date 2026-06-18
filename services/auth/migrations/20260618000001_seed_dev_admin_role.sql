-- +goose Up
-- +goose StatementBegin
--
-- Seed an org-scoped `admin` role for the dev `admin` user so the dashboard
-- works end-to-end on a fresh `make up`. Without this assignment the dev user
-- can log in but PENTEST-002's scope-aware RBAC and PENTEST-003's admin-only
-- user-creation gate would block them from doing anything useful (a flat
-- "any admin role anywhere" check is no longer sufficient).
--
-- Why this is a separate migration from 20260610000001_seed_dev_tenant.sql:
-- role_assignments is created later by 20260614000001_create_rbac.sql, so the
-- INSERT must run after that — goose enforces ordering by timestamp prefix.
--
-- Scope: "org" / "dev" — the admin can manage everything under `dev/*`. To
-- work in other orgs they would grant themselves additional roles through the
-- dashboard (which is itself how a real operator would onboard new tenants).
--
-- Bootstrap chicken-and-egg note: this is the only path that creates the
-- first admin in a fresh deployment. Production deployments should replace
-- this dev seed with a one-shot bootstrap script that creates the platform's
-- super-admin from operator-supplied credentials, then remove this migration.

INSERT INTO role_assignments (id, tenant_id, user_id, role_id, scope_type, scope_value, granted_by)
SELECT
    '00000000-0000-0000-0000-000000000020'::uuid,
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e'::uuid,
    '00000000-0000-0000-0000-000000000002'::uuid,
    id,
    'org',
    'dev',
    NULL
FROM roles
WHERE name = 'admin'
ON CONFLICT (user_id, role_id, scope_type, scope_value) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM role_assignments
WHERE id = '00000000-0000-0000-0000-000000000020';
-- +goose StatementEnd
