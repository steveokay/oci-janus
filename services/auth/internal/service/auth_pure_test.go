// Package service_test tests pure functions in the auth service that require no
// external dependencies — key encoding, JWKS helpers, and the rate-limit key builder.
package service

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

// TestEncodeExponent_standardValue checks that the public exponent 65537 (0x10001)
// round-trips through base64url encoding without leading zero bytes.
func TestEncodeExponent_standardValue(t *testing.T) {
	// 65537 is the overwhelmingly common RSA public exponent (AQAB in base64url).
	const e = 65537
	encoded := encodeExponent(e)
	if encoded == "" {
		t.Fatal("encodeExponent returned empty string")
	}
	// AQAB is the well-known representation of 65537 in base64url (RFC 7517).
	const want = "AQAB"
	if encoded != want {
		t.Errorf("encodeExponent(65537) = %q, want %q", encoded, want)
	}
}

// TestEncodeExponent_stripsLeadingZeros verifies that a small exponent whose
// big-endian encoding has leading zero bytes is correctly stripped.
func TestEncodeExponent_stripsLeadingZeros(t *testing.T) {
	// e = 3 in big-endian uint32 is [0x00, 0x00, 0x00, 0x03]. The output
	// should strip the two leading zero bytes and encode just [0x03].
	const e = 3
	encoded := encodeExponent(e)
	// Decode and verify only one byte was encoded.
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64url decode failed: %v", err)
	}
	if len(raw) != 1 {
		t.Errorf("expected 1 byte for exponent 3, got %d: %x", len(raw), raw)
	}
	if raw[0] != 3 {
		t.Errorf("expected byte value 3, got %d", raw[0])
	}
}

// TestEncodeExponent_roundTrip encodes then decodes an arbitrary exponent to
// confirm the big-endian value survives the round-trip.
func TestEncodeExponent_roundTrip(t *testing.T) {
	const e = 65537
	encoded := encodeExponent(e)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Pad back to 4 bytes so binary.BigEndian can read it.
	padded := make([]byte, 4)
	copy(padded[4-len(decoded):], decoded)
	got := int(binary.BigEndian.Uint32(padded))
	if got != e {
		t.Errorf("round-trip: got %d, want %d", got, e)
	}
}

// TestRsaToJWK_fields verifies that rsaToJWK produces the correct static fields
// and that N/E are non-empty base64url strings for a freshly generated key.
func TestRsaToJWK_fields(t *testing.T) {
	// Generate a small (1024-bit) key for test speed only — never use in production.
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const kid = "test-key-id"
	jwk := rsaToJWK(&priv.PublicKey, kid)

	if jwk.Kty != "RSA" {
		t.Errorf("Kty = %q, want %q", jwk.Kty, "RSA")
	}
	if jwk.Use != "sig" {
		t.Errorf("Use = %q, want %q", jwk.Use, "sig")
	}
	if jwk.Alg != "RS256" {
		t.Errorf("Alg = %q, want %q", jwk.Alg, "RS256")
	}
	if jwk.Kid != kid {
		t.Errorf("Kid = %q, want %q", jwk.Kid, kid)
	}
	if jwk.N == "" {
		t.Error("N is empty")
	}
	if jwk.E == "" {
		t.Error("E is empty")
	}
}

// TestRevokedKey_format checks the Redis key format used for JTI revocation so
// any future change is caught immediately.
func TestRevokedKey_format(t *testing.T) {
	const jti = "abc-123"
	got := revokedKey(jti)
	const want = "jwt:revoked:abc-123"
	if got != want {
		t.Errorf("revokedKey(%q) = %q, want %q", jti, got, want)
	}
}

// TestParsePrivateKey_invalidBase64 ensures parsePrivateKey returns an error for
// corrupt input without panicking.
func TestParsePrivateKey_invalidBase64(t *testing.T) {
	_, err := parsePrivateKey("!!!not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64 private key")
	}
}

// TestParsePublicKey_invalidBase64 ensures parsePublicKey returns an error for
// corrupt input without panicking.
func TestParsePublicKey_invalidBase64(t *testing.T) {
	_, err := parsePublicKey("!!!not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64 public key")
	}
}

// TestParsePrivateKey_emptyPEM ensures parsePrivateKey fails gracefully when the
// base64 payload decodes to data that contains no PEM block.
func TestParsePrivateKey_emptyPEM(t *testing.T) {
	// "aGVsbG8=" is base64("hello") — valid base64 but no PEM block.
	_, err := parsePrivateKey("aGVsbG8=")
	if err == nil {
		t.Error("expected error for input with no PEM block")
	}
}

// TestParsePublicKey_emptyPEM is the public-key variant of the above.
func TestParsePublicKey_emptyPEM(t *testing.T) {
	_, err := parsePublicKey("aGVsbG8=")
	if err == nil {
		t.Error("expected error for input with no PEM block")
	}
}
