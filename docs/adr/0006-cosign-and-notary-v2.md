# ADR-0006: Cosign + Notary v2

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Image signing has two viable ecosystems: Sigstore/Cosign (keyless, OIDC-backed) and Notary v2 (TUF-rooted, key-managed). They serve different threat models.

## Decision

Support both via `services/signer`: Cosign for keyless flows, Notary v2 for TUF-rooted enterprise flows. Both back onto the same `SIGNER_KEY_BACKEND` (Vault) abstraction (ADR-0014).

## Consequences

`services/signer/internal/signing/signer.go` carries both flows; deprecating either format is a breaking change for verifier clients.

## Verified by

`services/signer/internal/signing/signer.go` and `services/signer/internal/handler/grpc_test.go` (covers Cosign + Notary verify paths).
