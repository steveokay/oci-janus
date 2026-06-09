// Package argon2 provides argon2id password hashing and verification helpers.
// All passwords stored in the registry (user accounts, API keys) must be hashed
// with this package — never bcrypt, scrypt, or MD5.
package argon2

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Fixed parameters chosen to run in ~100ms on commodity hardware.
// Increasing memory or iterations requires a migration of existing hashes.
const (
	memory      uint32 = 64 * 1024 // 64 MiB
	iterations  uint32 = 3
	parallelism uint8  = 2
	saltLen            = 16
	keyLen      uint32 = 32
)

// Hash returns an argon2id PHC-format string for the given password.
// The format is: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// Verify reports whether password matches the stored argon2id encoded hash.
// Returns false (not an error) for wrong passwords; only returns an error
// when the hash itself is malformed.
func Verify(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid hash format")
	}

	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil {
		return false, fmt.Errorf("parse version: %w", err)
	}

	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("parse params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	stored, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	computed := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(stored)))
	return subtle.ConstantTimeCompare(computed, stored) == 1, nil
}
