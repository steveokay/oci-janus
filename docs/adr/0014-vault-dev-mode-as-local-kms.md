# ADR-0014: Vault dev mode as local KMS

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

Local dev needs a KMS to exercise the signing code path, but spinning up cloud KMS for every developer is impractical. Per-environment code paths invite drift.

## Decision

Use HashiCorp Vault dev mode locally with the same `SIGNER_KEY_BACKEND=vault` config as production. No dev-only fallback code path in `services/signer`.

## Consequences

The signing code path is identical in dev and prod, so bugs surface locally. Production switches Vault dev to Vault in HA mode without changing application code. Full doc lives in `docs/SIGNING.md`.

## Verified by

`services/signer/internal/signing/vault.go` — single Vault client used by both dev and prod; no per-environment branching.
