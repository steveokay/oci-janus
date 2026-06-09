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
