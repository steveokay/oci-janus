# ADR-0015: Audit table FORCE RLS + low-privilege `registry_audit_app` role

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

Standard RLS (ADR-0012) is bypassed by table owners. An audit trail that the application can rewrite is no audit trail — a compromised audit service must not be able to tamper with records.

## Decision

`audit_events` runs under `FORCE ROW LEVEL SECURITY` and is accessed via a dedicated `registry_audit_app` role that holds only `INSERT` + `SELECT` — no `UPDATE` or `DELETE`. The role is granted in a dedicated migration.

## Consequences

Even SQL injection inside the audit service cannot rewrite history. This decision is the precondition for ADR-0030 (deriving the hash-chain tip from `chain_seq` instead of a writable tip table).

## Verified by

`services/audit/migrations/20240101000002_audit_rls_role.sql` — creates `registry_audit_app` with INSERT-only grants and FORCE RLS.
