# ADR-0011: No presigned URLs to clients

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Presigned URLs are a common optimisation for blob upload/download but bypass audit, rate limiting, and tenant-scoped RBAC checks once issued.

## Decision

All blob traffic is proxied through `services/core` and `services/storage`. Clients never receive a direct object-store URL.

## Consequences

`services/storage` carries the bandwidth cost but every byte is auditable and revocable. Switching to presigned URLs would require re-implementing audit + rate limiting at the object-store edge.

## Verified by

`services/storage/internal/driver/driver.go` — driver interface exposes `Get`/`Put` but no `Presign` method.
