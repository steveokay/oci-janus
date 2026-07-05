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
