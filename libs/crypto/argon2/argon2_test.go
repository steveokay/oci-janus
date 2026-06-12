package argon2

import (
	"strings"
	"testing"
)

func TestHash_Verify_RoundTrip(t *testing.T) {
	hash, err := Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash format unexpected: %s", hash)
	}

	ok, err := Verify("correct-horse-battery-staple", hash)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("expected Verify to return true for correct password")
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	hash, _ := Hash("correct-horse-battery-staple")
	ok, err := Verify("wrong-password", hash)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("expected Verify to return false for wrong password")
	}
}

func TestHash_UniquePerCall(t *testing.T) {
	h1, _ := Hash("password")
	h2, _ := Hash("password")
	if h1 == h2 {
		t.Error("two hashes of the same password should differ due to random salt")
	}
}

func TestVerify_InvalidFormat(t *testing.T) {
	_, err := Verify("password", "not-a-hash")
	if err == nil {
		t.Error("expected error for malformed hash")
	}
}

// TestVerify_BadVersion verifies that a hash whose version field cannot be parsed
// returns an error rather than a silent mismatch.
func TestVerify_BadVersion(t *testing.T) {
	// Construct a PHC string with "v=NOTANUMBER" to exercise the Sscanf error path.
	_, err := Verify("password", "$argon2id$v=NOTANUMBER$m=65536,t=3,p=2$c2FsdA$aGFzaA")
	if err == nil {
		t.Error("expected error for non-numeric version field")
	}
}

// TestVerify_BadParams verifies that a hash with malformed m/t/p parameters
// is rejected before any cryptographic work is done.
func TestVerify_BadParams(t *testing.T) {
	_, err := Verify("password", "$argon2id$v=19$BADPARAMS$c2FsdA$aGFzaA")
	if err == nil {
		t.Error("expected error for malformed parameters field")
	}
}

// TestVerify_BadSaltEncoding verifies that an invalid base64-encoded salt
// returns a descriptive error.
func TestVerify_BadSaltEncoding(t *testing.T) {
	// Use valid version and params but invalid base64 for the salt segment.
	_, err := Verify("password", "$argon2id$v=19$m=65536,t=3,p=2$!!!badsalt!!!$aGFzaA")
	if err == nil {
		t.Error("expected error for invalid base64 salt")
	}
}

// TestVerify_BadHashEncoding verifies that an invalid base64-encoded hash value
// returns a descriptive error.
func TestVerify_BadHashEncoding(t *testing.T) {
	// Valid salt but invalid base64 for the hash segment.
	_, err := Verify("password", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$!!!badhash!!!")
	if err == nil {
		t.Error("expected error for invalid base64 hash")
	}
}
