# ADR-0024: Unified Bearer dispatch in `requireAuth` — JWT + `key.<id>.<secret>`

**Status:** ACCEPTED.
**Date:** 2026-06-23.
**Phase:** Initial.

## Context

CI bots and `curl` scripts wanted to introspect themselves directly without first exchanging an API key for a JWT. The alternatives were a parallel `/principal/me` route or two auth surfaces on every endpoint.

## Decision

One auth surface. The literal `key.` prefix is the structural discriminator that can't collide with a JWT (segment 0 starts with `eyJ` after base64-encoding `{`). Synthesised `*Claims` for API keys set `Roles: []` deliberately so role-gated handlers return 403, not 401.

## Consequences

One mental model for callers; role-gated endpoints continue to require a JWT. Any new authenticated handler inherits dispatch for free.

## Verified by

`services/auth/internal/handler/http.go:requireAuth` (the dispatch entry point) and `services/auth/internal/handler/http_apikey_auth_test.go` (covers both branches).
