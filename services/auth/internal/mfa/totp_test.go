// Package mfa unit tests — TOTP generation/validation with replay counters and
// backup-code generation. Pure crypto, no DB.
package mfa

import (
	"encoding/base32"
	"testing"
	"time"

	"github.com/pquerna/otp/hotp"
)

// rfc6238Secret is the RFC 6238 Appendix B seed ("12345678901234567890").
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// TestValidateCode_MatchesGeneratedCode round-trips: a code generated for the
// current window validates and returns that window's counter.
func TestValidateCode_MatchesGeneratedCode(t *testing.T) {
	at := time.Unix(59, 0) // RFC 6238 test time T1
	counter := uint64(at.Unix()) / periodSeconds
	code, err := hotp.GenerateCode(rfc6238Secret, counter)
	if err != nil {
		t.Fatalf("seed code: %v", err)
	}
	ok, gotCounter := ValidateCode(rfc6238Secret, code, at)
	if !ok {
		t.Fatal("ValidateCode must accept a freshly generated code")
	}
	if gotCounter != int64(counter) {
		t.Fatalf("counter mismatch: got %d want %d", gotCounter, counter)
	}
}

// TestValidateCode_AcceptsPrevWindow allows a code from one step earlier
// (±1 skew), returning that earlier counter.
func TestValidateCode_AcceptsPrevWindow(t *testing.T) {
	at := time.Unix(1234567890, 0)
	prev := uint64(at.Unix())/periodSeconds - 1
	code, _ := hotp.GenerateCode(rfc6238Secret, prev)
	ok, gotCounter := ValidateCode(rfc6238Secret, code, at)
	if !ok || gotCounter != int64(prev) {
		t.Fatalf("prev-window code should validate with its counter, got ok=%v c=%d", ok, gotCounter)
	}
}

// TestValidateCode_AcceptsNextWindow allows a code from one step later
// (+1 skew), returning that later counter — the forward half of the ±1
// tolerance (the prev-window case covers −1).
func TestValidateCode_AcceptsNextWindow(t *testing.T) {
	at := time.Unix(1234567890, 0)
	next := uint64(at.Unix())/periodSeconds + 1
	code, _ := hotp.GenerateCode(rfc6238Secret, next)
	ok, gotCounter := ValidateCode(rfc6238Secret, code, at)
	if !ok || gotCounter != int64(next) {
		t.Fatalf("next-window code should validate with its counter, got ok=%v c=%d", ok, gotCounter)
	}
}

// TestValidateCode_RejectsOutOfWindow rejects codes exactly one step beyond the
// ±1 tolerance on each side (−2 and +2), pinning the accepted window to three
// counters and no wider.
func TestValidateCode_RejectsOutOfWindow(t *testing.T) {
	at := time.Unix(1234567890, 0)
	base := uint64(at.Unix()) / periodSeconds
	for _, delta := range []int{-2, +2} {
		counter := uint64(int64(base) + int64(delta))
		code, _ := hotp.GenerateCode(rfc6238Secret, counter)
		if ok, _ := ValidateCode(rfc6238Secret, code, at); ok {
			t.Fatalf("code from delta=%d must be rejected (outside ±%d skew)", delta, skewSteps)
		}
	}
}

// TestValidateCode_RejectsWrongCode rejects a bogus code.
func TestValidateCode_RejectsWrongCode(t *testing.T) {
	if ok, _ := ValidateCode(rfc6238Secret, "000000", time.Unix(59, 0)); ok {
		t.Fatal("ValidateCode must reject an invalid code")
	}
}

// TestGenerateSecret_ProducesValidBase32AndURI mints a 20-byte base32 secret
// and an otpauth:// URI carrying it.
func TestGenerateSecret_ProducesValidBase32AndURI(t *testing.T) {
	secret, uri, err := GenerateSecret("oci-janus", "alice@example.com")
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(raw) != 20 {
		t.Fatalf("secret must be 20 raw bytes of base32, got len=%d err=%v", len(raw), err)
	}
	if len(uri) < 10 || uri[:10] != "otpauth://" {
		t.Fatalf("uri must be an otpauth:// URI, got %q", uri)
	}
}

// TestGenerateBackupCodes_Returns8Distinct returns 8 distinct, non-empty codes.
func TestGenerateBackupCodes_Returns8Distinct(t *testing.T) {
	codes, err := GenerateBackupCodes()
	if err != nil {
		t.Fatalf("GenerateBackupCodes: %v", err)
	}
	if len(codes) != backupCodeCount {
		t.Fatalf("want %d codes, got %d", backupCodeCount, len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if c == "" || seen[c] {
			t.Fatalf("codes must be non-empty and distinct, got %q", c)
		}
		seen[c] = true
	}
}
