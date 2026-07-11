# ADR-0029: AES-256-GCM ciphertext carries a `Version = 0x01` prefix for future KEK rotation

**Status:** ACCEPTED.
**Date:** 2026-06-30.
**Phase:** REDESIGN-001 Phase 6.4.

## Context

Encrypted secrets at rest (OAuth client secrets, SAML SP private keys, upstream proxy creds) had no versioning. A future KEK rotation would need to distinguish ciphertexts encrypted under the old vs new key — without a version byte, every row would need scanning + re-encrypting in lockstep.

## Decision

`libs/crypto/aes` ciphertext layout is `version || nonce(12) || ciphertext || tag(16)` with `Version = 0x01`. Decrypt is "try v1 then fall back to legacy" because ~1/256 legacy ciphertexts have random nonces starting with `0x01` and strict dispatch would break those rows; GCM auth-tag verification preserves tamper safety on both branches.

## Consequences

The rotation tool ships separately and **has now shipped** as RED-FU-015 (PR
#249; operator runbook [`infra/runbooks/kek-rotation.md`](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/kek-rotation.md)).

**Correction (RED-FU-015 scoping).** The original assumption that a rotation
would "flip the version byte" to distinguish old-vs-new-key ciphertexts proved
wrong: the version byte encodes the *layout* (`v1 = version‖nonce‖ct‖tag`), not
*which key* produced the ciphertext — a re-encrypted row stays `0x01`. So you
cannot tell a row's KEK generation from its bytes. The shipped tool instead
detects completion by **trial-decryption** (authoritative) plus a nullable
`kek_version SMALLINT` audit column, and re-encrypts in place. The version byte
remains valuable for the *legacy-vs-v1 layout* fallback it was built for; it is
just not the rotation key-discriminator this ADR first envisioned.

## Verified by

`libs/crypto/aes/aes.go:Version` constant and `libs/crypto/aes/aes_test.go:TestDecrypt_LegacyWithVersionByteCollision` (the dual-decode fallback).
