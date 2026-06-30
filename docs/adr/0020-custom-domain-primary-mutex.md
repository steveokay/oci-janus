# ADR-0020: Custom-domain primary mutex on `tenant_domains.is_primary`

**Status:** SUPERSEDED by ADR-0025 (2026-06-26).
**Date:** 2026-06-20.
**Phase:** Initial.

## Context

Tenants could attach multiple custom domains but the gateway needed exactly one canonical primary for redirects + cert provisioning. A naive implementation could observe two primaries mid-swap.

## Decision

Partial unique index `WHERE is_primary`; `MarkDomainVerified` auto-promotes the first verified domain; primary swap is one atomic tx (`SELECT verified → demote-all → promote-target RETURNING`).

## Consequences

No observable state ever had two primaries. ADR-0025 removed the custom-domain surface entirely (RM-001), so this mutex no longer has a caller.

## Verified by (legacy)

`tenant_domains` table + `is_primary` column — removed by `services/tenant/migrations/20260626000001_drop_tenant_domains.sql`.

## Verified by (current)

No current verifier — custom domains are not part of the single-tenant deployment posture. See ADR-0025.
