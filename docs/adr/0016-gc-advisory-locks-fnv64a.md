# ADR-0016: GC advisory locks via `pg_try_advisory_lock` (FNV-64a key)

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

Multiple GC workers across replicas must not run the same tenant's mark-sweep concurrently, but blocking on a row lock would serialise unrelated tenants.

## Decision

Use `pg_try_advisory_lock` keyed by FNV-64a hash of the tenant id. Non-blocking — a contended worker simply skips that tenant and tries the next. Lock release is deferred so a panic still releases.

## Consequences

GC scales horizontally without classical-queue coordination. Switching the keying scheme (or the lock primitive) would risk lock collisions across tenants.

## Verified by

`services/gc/internal/advisory/lock.go` — the FNV-64a key derivation + `pg_try_advisory_lock` call.
