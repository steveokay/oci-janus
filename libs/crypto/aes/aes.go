// Package aes provides AES-256-GCM authenticated encryption helpers.
// Used by registry-proxy to encrypt upstream credentials at rest and by
// any service storing secrets in the database.
//
// Ciphertext layout (REDESIGN-001 Phase 6.4):
//
//	v1 (current, produced by Encrypt):
//	    version(1) || nonce(12) || ciphertext || tag(16)
//	    where version = 0x01.
//
//	legacy (pre-v1, still accepted by Decrypt for back-compat):
//	    nonce(12) || ciphertext || tag(16)
//
// The leading version byte lets the operator detect rows encrypted under an
// older KEK and rotate them in place when MASTER_KEK is bumped, without
// breaking existing rows.
//
// Decrypt dispatches as follows:
//  1. If the first byte equals a known Version AND the payload is long
//     enough to be a valid v1 row, try the v1 codec first.
//  2. If v1 parsing fails (or the leading byte isn't a known version),
//     fall back to the legacy codec.
//
// Silent-downgrade safety: a tampered v1 row can only "succeed" on the
// legacy fallback if its bytes also pass GCM authentication under the
// legacy layout, which requires forging a GCM tag — computationally
// infeasible. So back-compat is preserved without weakening authenticity.
//
// Why the fallback (not strict-on-version): legacy rows have a random
// 12-byte nonce, so ~1/256 of them happen to start with 0x01. A strict
// "first byte is Version => must be v1" rule would mis-route those rows
// and fail decryption. Always-try-v1-then-legacy avoids that landmine.
package aes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Version is the current ciphertext layout marker prepended by Encrypt.
// Incrementing this requires a paired Decrypt branch that knows how to parse
// the new layout. Never reuse a retired version byte.
const Version byte = 0x01

// gcmTagSize is the AEAD tag length appended by AES-GCM Seal.
// Exposed as a const so the legacy-vs-v1 length heuristic in Decrypt stays
// self-documenting.
const gcmTagSize = 16

// Encrypt encrypts plaintext with AES-256-GCM using the provided key.
// key must be exactly 32 bytes. The returned blob is laid out as
// `Version || nonce(12) || ciphertext || tag(16)` so Decrypt can recover the
// nonce and pick the correct codec without side-channel storage.
func Encrypt(plaintext, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aes key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	// Allocate output with the version byte already in place. We then place
	// the nonce immediately after it and let Seal append ct+tag.
	nonceSize := gcm.NonceSize()
	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+gcmTagSize)
	out[0] = Version
	nonce := out[1 : 1+nonceSize]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends ciphertext+tag to `out` (which already holds version+nonce).
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt decrypts AES-256-GCM ciphertext produced by Encrypt, or by any
// pre-v1 caller of this package (legacy layout). key must be exactly 32
// bytes. Returns an error only if neither layout authenticates — at which
// point the ciphertext was tampered with, the key is wrong, or the payload
// is malformed.
//
// See the package doc comment for full layout + dispatch rules.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aes key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()

	// A payload too short to hold even a nonce is unconditionally invalid —
	// short-circuit here so the codecs below can assume a sensible length.
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short to contain nonce")
	}

	// Try v1 first when the leading byte matches Version and the payload is
	// at least version(1) + nonce(12) + tag(16) long. If GCM auth fails we
	// fall through to the legacy attempt — this is safe because legacy auth
	// would also fail on a tampered v1 row (forging a second valid GCM tag
	// under a different layout is computationally infeasible).
	if len(ciphertext) >= 1+nonceSize+gcmTagSize && ciphertext[0] == Version {
		nonce := ciphertext[1 : 1+nonceSize]
		ct := ciphertext[1+nonceSize:]
		if plaintext, err := gcm.Open(nil, nonce, ct, nil); err == nil {
			return plaintext, nil
		}
		// Intentional fall-through to legacy. If both codecs fail we return
		// the legacy error below so callers see a single consistent message.
	}

	// Legacy layout: nonce(12) || ciphertext || tag(16). Reached when the
	// first byte isn't Version, the payload is too short for v1, or v1
	// authentication failed and we're attempting the back-compat path.
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt/verify tag: %w", err)
	}
	return plaintext, nil
}
