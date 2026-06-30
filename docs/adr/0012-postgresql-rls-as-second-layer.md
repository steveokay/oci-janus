# ADR-0012: PostgreSQL RLS as second layer

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Application-level `WHERE tenant_id = ?` filters are a single line of defence — a missed filter in a new query leaks cross-tenant rows. Defence in depth requires the database to enforce isolation too.

## Decision

Enable `ROW LEVEL SECURITY` on every tenant-scoped table; the application sets `SET LOCAL app.tenant_id = '<id>'` per transaction; policies compare `tenant_id = current_setting('app.tenant_id')::uuid`.

## Consequences

A bug in application code cannot leak rows — Postgres rejects the read. For audit, FORCE RLS is applied with a low-privilege role (ADR-0015) so even the application role cannot bypass the policy.

## Verified by

`services/audit/migrations/20240101000002_audit_rls_role.sql` — sets up `registry_audit_app` role + FORCE RLS pattern used as the reference implementation.
