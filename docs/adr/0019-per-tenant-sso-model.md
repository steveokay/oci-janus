# ADR-0019: Per-tenant SSO model (OAuth PKCE + SAML 2.0 SP)

**Status:** SUPERSEDED by ADR-0027 (2026-06-28).
**Date:** 2026-06-21.
**Phase:** Initial.

## Context

The SaaS model assumed each tenant brought their own IdP, so SSO configuration was a per-tenant table.

## Decision

`auth_providers` + `auth_login_sessions` tables on `services/auth`. Hand-rolled OAuth (PKCE S256, no `x/oauth2`); SAML wraps `crewjam/saml` bare `ServiceProvider`. `client_secret` AES-256-GCM-encrypted before persistence.

## Consequences

Per-tenant SSO admin RPCs + UI surfaces. REDESIGN-001 Phase 2.2 concluded self-hosters have one IdP, not one per tenant; ADR-0027 collapses this into a single `global_sso_config` row and removes the per-tenant admin RPCs (RM-003).

## Verified by (legacy)

`services/auth/migrations/20260628000003_drop_auth_providers.sql` — the table is gone; only the drop migration remains.

## Verified by (current)

`services/auth/internal/repository/global_sso_config.go` — the replacement single-row config.
