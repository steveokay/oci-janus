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
