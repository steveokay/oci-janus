package aes

import (
	"bytes"
	"testing"
)

var testKey = []byte("01234567890123456789012345678901") // 32 bytes

func TestEncrypt_Decrypt_RoundTrip(t *testing.T) {
	plaintext := []byte("registry upstream password")
	ct, err := Encrypt(plaintext, testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(ct, testKey)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestEncrypt_UniqueNonce(t *testing.T) {
	pt := []byte("same plaintext")
	ct1, _ := Encrypt(pt, testKey)
	ct2, _ := Encrypt(pt, testKey)
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext should differ due to random nonce")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	ct, _ := Encrypt([]byte("secret"), testKey)
	wrongKey := []byte("99999999999999999999999999999999")
	_, err := Decrypt(ct, wrongKey)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	ct, _ := Encrypt([]byte("secret"), testKey)
	ct[len(ct)-1] ^= 0xff // flip last byte
	_, err := Decrypt(ct, testKey)
	if err == nil {
		t.Error("expected error for tampered ciphertext")
	}
}

func TestEncrypt_BadKeyLength(t *testing.T) {
	_, err := Encrypt([]byte("data"), []byte("short"))
	if err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

// TestDecrypt_BadKeyLength verifies Decrypt also enforces the 32-byte key requirement.
func TestDecrypt_BadKeyLength(t *testing.T) {
	_, err := Decrypt([]byte("any-ciphertext"), []byte("tooshort"))
	if err == nil {
		t.Error("expected error for non-32-byte key in Decrypt")
	}
}

// TestDecrypt_TooShortCiphertext verifies that a ciphertext shorter than the
// GCM nonce size is rejected before any decryption attempt.
func TestDecrypt_TooShortCiphertext(t *testing.T) {
	// GCM nonce is 12 bytes; any ciphertext <= 12 bytes cannot contain a nonce + ciphertext.
	shortCT := make([]byte, 4)
	_, err := Decrypt(shortCT, testKey)
	if err == nil {
		t.Error("expected error for ciphertext shorter than nonce size")
	}
}

// TestEncrypt_EmptyPlaintext verifies that an empty plaintext can be encrypted
// and then successfully decrypted back to an empty byte slice.
func TestEncrypt_EmptyPlaintext(t *testing.T) {
	ct, err := Encrypt([]byte{}, testKey)
	if err != nil {
		t.Fatalf("Encrypt empty plaintext: %v", err)
	}
	pt, err := Decrypt(ct, testKey)
	if err != nil {
		t.Fatalf("Decrypt empty plaintext: %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(pt))
	}
}
