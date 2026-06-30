# ADR-0003: JWT RS256 + API keys + mTLS

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Authentication needed to cover three callers: browsers, machine accounts, and intra-cluster service calls — none of which is well served by a single mechanism.

## Decision

Defence in depth: mTLS guards the network layer for all internal gRPC; RS256 JWTs (300s TTL, JTI revocation in Redis) carry user identity; argon2-hashed API keys serve machine accounts.

## Consequences

Three validators must stay in lockstep — `requireAuth` dispatches JWT vs API-key on the `key.` prefix (see ADR-0024); `libs/auth/mtls` builds the network-layer config. Dropping any layer collapses the model.

## Verified by

`libs/auth/mtls/mtls.go:ServerTLSConfig` plus `services/auth/internal/handler/http.go:requireAuth` — both must exist for the layered model to hold.
