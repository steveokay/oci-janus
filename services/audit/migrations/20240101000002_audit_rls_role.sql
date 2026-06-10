-- +goose Up
-- +goose StatementBegin

-- registry_audit_app: low-privilege runtime role for the audit service.
-- The service must connect as this role (via SET ROLE) at runtime. Connecting as
-- the schema owner is rejected by the startup check (SEC-001).
CREATE ROLE registry_audit_app NOLOGIN;

-- Grant the role to whoever runs migrations so the pool can SET ROLE at runtime.
GRANT registry_audit_app TO CURRENT_USER;

-- INSERT + SELECT through the parent table.
GRANT INSERT, SELECT ON audit_events TO registry_audit_app;

-- DELETE on the default partition only — used by the retention purge path
-- (PurgeOlderThan targets audit_events_default directly, which bypasses the
-- parent-level RLS by design; this is the only authorised deletion path).
GRANT DELETE ON audit_events_default TO registry_audit_app;

-- Enable RLS and force it for all roles including the table owner.
-- Without FORCE, the schema owner silently bypasses all policies.
ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;

-- INSERT: any row may be inserted (tenant validation is in application code).
CREATE POLICY audit_insert ON audit_events
    AS PERMISSIVE FOR INSERT
    WITH CHECK (true);

-- SELECT: unrestricted at the row level; tenant isolation is enforced via
-- parameterised WHERE clauses in the repository layer.
CREATE POLICY audit_select ON audit_events
    AS PERMISSIVE FOR SELECT
    USING (true);

-- No UPDATE or DELETE policies are defined. Under FORCE ROW LEVEL SECURITY
-- the default-deny applies to all roles including the table owner, making the
-- audit table effectively append-only through the parent relation.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS audit_select ON audit_events;
DROP POLICY IF EXISTS audit_insert ON audit_events;
ALTER TABLE audit_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_events DISABLE ROW LEVEL SECURITY;
REVOKE DELETE ON audit_events_default FROM registry_audit_app;
REVOKE INSERT, SELECT ON audit_events FROM registry_audit_app;
REVOKE registry_audit_app FROM CURRENT_USER;
DROP ROLE IF EXISTS registry_audit_app;

-- +goose StatementEnd
