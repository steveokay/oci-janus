# ADR-0004: Multi-tenant with custom domains

**Status:** SUPERSEDED by ADR-0025 (2026-06-26).
**Date:** Initial.
**Phase:** Initial.

## Context

The platform was originally scoped as a SaaS-style multi-tenant registry, with white-label custom-domain support for enterprise customers.

## Decision

Every row carried `tenant_id`; `services/tenant` owned a `tenant_domains` table with DNS TXT challenge verification; the gateway routed by host header to a tenant.

## Consequences

REDESIGN-001 Phase 0 found the SaaS surface (custom domains, per-tenant SSO, tenant signup, plan/billing UI) was unused by the dominant self-hoster use case. ADR-0025 shifts default deployment to single-tenant and removes the custom-domain surface entirely (RM-001).

## Verified by (legacy)

`services/tenant/internal/repository/domains.go` and `tenant_domains` table — removed; only the drop migration `services/tenant/migrations/20260626000001_drop_tenant_domains.sql` remains as evidence.

## Verified by (current)

`libs/config/loader/loader.go:DeploymentMode` + the `single` default in the loader.
