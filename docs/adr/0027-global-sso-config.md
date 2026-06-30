# ADR-0027: Global SSO config replaces per-tenant `auth_providers`

**Status:** ACCEPTED.
**Date:** 2026-06-28.
**Phase:** REDESIGN-001 Phase 2.2.

## Context

Self-hosters have one IdP, not one per tenant. The per-tenant `auth_providers` table + admin RPCs were SaaS-only complexity that ADR-0025 made vestigial.

## Decision

New `global_sso_config` table holds the single configuration row. OAuth `client_secret` + SAML SP private key are AES-256-GCM-encrypted with the versioned ciphertext prefix from ADR-0029. Per-tenant SSO admin surfaces removed (RM-003).

## Consequences

Supersedes ADR-0019. Drop migrations remove `auth_providers`; admin RPCs + frontend SSO panel were removed in Phase 4.7.

## Verified by

`services/auth/migrations/20260628000001_global_sso_config.sql` and `services/auth/internal/repository/global_sso_config.go`.
