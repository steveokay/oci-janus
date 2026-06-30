# ADR-0026: `users.is_global_admin` typed primitive replaces `(admin, org, '*')` marker scope

**Status:** ACCEPTED.
**Date:** 2026-06-28.
**Phase:** REDESIGN-001 Phase 5.1.

## Context

Platform-admin was encoded as a magic-string `scope_value = '*'` on an `org`-scoped role assignment. Every gate had to special-case the string, which produced SEC-022 (marker leak) and SEC-026 ("any-org-admin clears workspace admin" scope creep).

## Decision

`users.is_global_admin BOOLEAN` is a typed column. `effectiveGlobalAdmin(r)` short-circuits gates BEFORE role-assignment lookup. In single mode every workspace admin is an effective global admin. Phase 5.1 backfill deleted marker grants without granting equivalent tenant-scoped admin rows.

## Consequences

Eliminates the marker-leak class of bugs; gates become readable typed checks. Phase 5.1 tail PRs (#193 / #197 / #198) wired the fast path through every workspace gate.

## Verified by

`services/auth/migrations/20260629000001_users_is_global_admin.sql` and `services/auth/internal/repository/user.go` (IsGlobalAdmin field on the user row).
