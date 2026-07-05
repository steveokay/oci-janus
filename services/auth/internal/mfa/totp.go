// Package mfa wraps pquerna/otp to provide the TOTP primitives the auth
// service needs for two-factor auth: minting an enrolment secret + otpauth URI,
// validating a code against a ±1 time-step window while returning the matched
// counter (for replay prevention), and generating single-use backup codes.
// This package holds NO database or HTTP logic.
package mfa

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

const (
	// periodSeconds is the TOTP time step (RFC 6238 default).
	periodSeconds = 30
	// secretBytes is the raw shared-secret length (160-bit, RFC 6238 minimum).
	secretBytes = 20
	// digits is the OTP length.
	digits = otp.DigitsSix
	// skewSteps is how many periods on each side of "now" are accepted, to
	// tolerate client/server clock drift.
	skewSteps = 1
	// backupCodeCount is how many single-use recovery codes an enrolment mints.
	backupCodeCount = 8
	// backupCodeBytes is the entropy per backup code before base32 encoding.
	backupCodeBytes = 10
)

// GenerateSecret mints a fresh 20-byte base32 TOTP secret plus its otpauth://
// provisioning URI (issuer + account label). The FE renders a QR from the URI.
// The returned secret is the value stored (encrypted) at rest.
func GenerateSecret(issuer, account string) (secretBase32, otpauthURI string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		SecretSize:  secretBytes,
		Digits:      digits,
		Period:      periodSeconds,
	})
	if err != nil {
		return "", "", fmt.Errorf("generate totp secret: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// ValidateCode reports whether code is a valid TOTP for secretBase32 at time t,
// within ±skewSteps. On success it returns the matched time-step counter so the
// caller can enforce replay prevention (reject counters <= the last accepted).
// The comparison is constant-time.
func ValidateCode(secretBase32, code string, t time.Time) (ok bool, counter int64) {
	base := uint64(t.Unix()) / periodSeconds
	for delta := -skewSteps; delta <= skewSteps; delta++ {
		c := base + uint64(delta) //nolint:gosec // bounded by skewSteps
		want, err := hotp.GenerateCodeCustom(secretBase32, c, hotp.ValidateOpts{
			Digits:    digits,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true, int64(c)
		}
	}
	return false, 0
}

// GenerateBackupCodes returns backupCodeCount distinct base32 recovery codes.
// Callers argon2-hash these before storage and surface the plaintext to the
// user exactly once.
func GenerateBackupCodes() ([]string, error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	out := make([]string, 0, backupCodeCount)
	seen := make(map[string]struct{}, backupCodeCount)
	for len(out) < backupCodeCount {
		buf := make([]byte, backupCodeBytes)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("read random for backup code: %w", err)
		}
		code := enc.EncodeToString(buf)
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out, nil
}
