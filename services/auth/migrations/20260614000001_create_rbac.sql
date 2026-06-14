-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT ''
);

INSERT INTO roles (id, name, description) VALUES
    ('a0000001-0000-0000-0000-000000000001', 'owner',  'Full control including delete and member management'),
    ('a0000001-0000-0000-0000-000000000002', 'admin',  'Can push, pull, delete, and manage members'),
    ('a0000001-0000-0000-0000-000000000003', 'writer', 'Can push and pull'),
    ('a0000001-0000-0000-0000-000000000004', 'reader', 'Can pull only')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS role_assignments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     UUID NOT NULL REFERENCES roles(id),
    scope_type  TEXT NOT NULL CHECK (scope_type IN ('org', 'repo')),
    scope_value TEXT NOT NULL,
    granted_by  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, role_id, scope_type, scope_value)
);

CREATE INDEX IF NOT EXISTS idx_role_assignments_user_tenant
    ON role_assignments (user_id, tenant_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS role_assignments;
DROP TABLE IF EXISTS roles;
-- +goose StatementEnd
