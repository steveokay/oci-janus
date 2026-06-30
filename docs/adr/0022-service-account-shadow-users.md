# ADR-0022: Service-account principal pattern — shadow users

**Status:** ACCEPTED.
**Date:** 2026-06-22.
**Phase:** Initial.

## Context

API keys for machine accounts needed a stable subject id for RBAC, audit, RLS, and JWT claims — but introducing a parallel "principal" type would have doubled the auth code surface.

## Decision

Each service account auto-provisions a `users.kind='service_account'` row. `ValidateAPIKey`/`ValidateToken` return that id in `user_id`; downstream services treat it as an opaque actor. Distinguishing principal kind is a read-path concern (`LEFT JOIN users ON kind`), not a write-path one.

## Consequences

RBAC, audit, RLS, and JWT machinery stayed unchanged. Downstream services do not need to know what a service account is.

## Verified by

`services/auth/migrations/20260622000001_user_kind.sql` and `services/auth/internal/repository/service_account.go`.
