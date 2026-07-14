# ADR-0031: Retire the `multi` deployment posture — single-tenant is the only mode

**Status:** ACCEPTED (implementation tracked as REDESIGN-001 Phase 9).
**Date:** 2026-07-14.
**Phase:** REDESIGN-001 Phase 9.

## Context

ADR-0025 made `single` the default but kept `DEPLOYMENT_MODE=multi` as a dormant SaaS posture "in case." A 2026-07-14 product decision made single-tenant the **permanent** direction — there is no future multi-tenant/SaaS pivot — so the dormant `multi` branches are now pure dead weight (a config flag, `== multi` branches, BFF admin-tenants CRUD, and FE tenant-management chrome) plus a standing misconfiguration risk (a flag flip re-exposes an unsupported SaaS surface).

## Decision

Remove the `multi` posture entirely. Delete `DEPLOYMENT_MODE` and every `== multi` branch, the BFF admin-tenants CRUD routes, the `/api/v1/deployment-info` mode field + `useDeploymentInfo` gating, the FE Settings→Platform *tenants* section + tenant create/delete/detail dialogs + tenant switcher + topbar multi-chip, and the multi-only tests.

**Keep** the single-tenant machinery and make it unconditional (not flag-gated): `SingleTenantInjector`, `deployment_metadata` + `GetDeploymentMetadata`, `bootstrap.FetchTenantID`, the `registry-auth bootstrap` CLI, and the `CreateTenant` RPC (bootstrap-only; its "reject a 2nd tenant" guard becomes always-on rather than `mode`-gated). **Keep** `tenant_id` columns/proto fields frozen as the bootstrap-tenant constant — the data model is untouched (this is *not* a `tenant_id` removal; that was rejected as high-risk / near-zero gain).

## Consequences

Supersedes the "keep `multi` as opt-in" half of ADR-0025 (0025's single-tenant-by-default posture stands). ~1500–2000 LOC removed across 6 areas, LOW risk (paths are already cleanly mode-separated). CLAUDE.md §1/§9 and `docs/SERVICES.md` lose their dual-mode language once Phase 9 ships (code changes first, then the rules follow). Scanner/GC/retention keep their existing single-mode homes (Settings→Scanning/Housekeeping); only the Platform *tenants* surface is lost.

## Verified by

`.claude/plans/2026-06-26-single-tenant-redesign.md` Phase 9 (delete-list + sequencing). Code pointer lands with Phase 9: removal of the `DeploymentMode` field from `libs/config/loader/loader.go`.
