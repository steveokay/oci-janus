# ADR-0025: Self-hosted single-tenant by default; `DEPLOYMENT_MODE=single|multi`

**Status:** ACCEPTED; the "keep `multi` as opt-in" half SUPERSEDED by ADR-0031 (2026-07-14). The single-tenant-by-default posture stands.
**Date:** 2026-06-26.
**Phase:** REDESIGN-001 Phase 0.

## Context

A 2026-06-26 system review found significant drift between CLAUDE.md's multi-tenant SaaS claims and the actual codebase. Fixing forward (full SaaS) or backward (rip out `tenant_id`) were both expensive; the dominant OSS use case was single-tenant self-hosted.

## Decision

Default `DEPLOYMENT_MODE=single` (self-hosted single-tenant); `multi` preserves the SaaS surface. Custom domains, per-tenant SSO, tenant signup, plan/billing UI removed. Single mode auto-bootstraps one tenant via `registry-auth bootstrap` (ADR-0028) + `tenant.deployment_metadata.bootstrap_tenant_id`.

## Consequences

Supersedes ADR-0004 (multi-tenant custom domains) and motivates ADR-0026 (typed global-admin) + ADR-0027 (global SSO) + ADR-0028 (bootstrap CLI). The `tenant_id` columns stay so `multi` mode remains viable.

## Verified by

`libs/config/loader/loader.go` — `deployment_mode` config field with `single` as default.
