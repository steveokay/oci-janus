// Package rekey provides KEK-rotation primitives: re-encrypting an
// AES-256-GCM ciphertext from an old key-encryption key (KEK) to a new one,
// and the declarative sweep engine + CLI runner used by each service's
// `rotate-kek` subcommand (RED-FU-015).
//
// The crypto core composes the two existing single-key calls in
// libs/crypto/aes — it deliberately does not touch the AES codec itself, so
// the ciphertext layout is unchanged and re-encrypted rows stay v1.
package rekey

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
)

// keyLen is the required KEK length in bytes (AES-256).
const keyLen = 32

// Rekey re-encrypts one ciphertext from oldKey to newKey. It returns the new
// ciphertext, or an error if the cell does not decrypt under oldKey (wrong
// key, corrupt, or tampered — a GCM authentication failure). The plaintext is
// never returned or logged.
func Rekey(oldKey, newKey, ciphertext []byte) ([]byte, error) {
	plain, err := aes.Decrypt(ciphertext, oldKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt under old key: %w", err)
	}
	out, err := aes.Encrypt(plain, newKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt under new key: %w", err)
	}
	return out, nil
}

// OnNewKey reports whether a ciphertext already decrypts under newKey. It is
// the authoritative "is this row done?" check used by --verify: a row that
// returns true needs no rotation.
func OnNewKey(newKey, ciphertext []byte) bool {
	_, err := aes.Decrypt(ciphertext, newKey)
	return err == nil
}

// ParseKeyHex decodes a hex-encoded KEK and validates it is exactly 32 bytes.
// Surrounding whitespace (e.g. a trailing newline from a piped env var) is
// trimmed. The key material is never logged by callers.
func ParseKeyHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("key is not valid hex: %w", err)
	}
	if len(b) != keyLen {
		return nil, fmt.Errorf("key must be %d bytes (%d hex chars), got %d", keyLen, keyLen*2, len(b))
	}
	return b, nil
}

// GenerateKeyHex mints a fresh 32-byte KEK from crypto/rand and returns it
// hex-encoded, ready to paste into a secrets manager. Used by --generate.
func GenerateKeyHex() (string, error) {
	b := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
