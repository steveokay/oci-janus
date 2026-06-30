# ADR-0029: AES-256-GCM ciphertext carries a `Version = 0x01` prefix for future KEK rotation

**Status:** ACCEPTED.
**Date:** 2026-06-30.
**Phase:** REDESIGN-001 Phase 6.4.

## Context

Encrypted secrets at rest (OAuth client secrets, SAML SP private keys, upstream proxy creds) had no versioning. A future KEK rotation would need to distinguish ciphertexts encrypted under the old vs new key — without a version byte, every row would need scanning + re-encrypting in lockstep.

## Decision

`libs/crypto/aes` ciphertext layout is `version || nonce(12) || ciphertext || tag(16)` with `Version = 0x01`. Decrypt is "try v1 then fall back to legacy" because ~1/256 legacy ciphertexts have random nonces starting with `0x01` and strict dispatch would break those rows; GCM auth-tag verification preserves tamper safety on both branches.

## Consequences

Future KEK rotation can flip the version byte and migrate gradually. The actual rotation tool ships separately.

## Verified by

`libs/crypto/aes/aes.go:Version` constant and `libs/crypto/aes/aes_test.go:TestDecrypt_LegacyWithVersionByteCollision` (the dual-decode fallback).
