// Package rekey unit tests — pure crypto core, no database.
package rekey

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
)

// key32 returns a deterministic 32-byte key whose bytes are all b.
func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestRekey_RoundTrip(t *testing.T) {
	oldKey, newKey := key32(0x11), key32(0x22)
	plaintext := []byte("super-secret-oauth-client-secret")

	oldCT, err := aes.Encrypt(plaintext, oldKey)
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}

	newCT, err := Rekey(oldKey, newKey, oldCT)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	got, err := aes.Decrypt(newCT, newKey)
	if err != nil {
		t.Fatalf("decrypt under new key: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}

	if _, err := aes.Decrypt(newCT, oldKey); err == nil {
		t.Fatal("re-encrypted ciphertext must not decrypt under the old key")
	}
}

func TestRekey_WrongOldKeyFails(t *testing.T) {
	realOld, wrongOld, newKey := key32(0x11), key32(0x99), key32(0x22)
	ct, _ := aes.Encrypt([]byte("x"), realOld)

	if _, err := Rekey(wrongOld, newKey, ct); err == nil {
		t.Fatal("Rekey must fail when the ciphertext does not decrypt under oldKey")
	}
}

// TestRekey_EmptyAndNilCiphertextFail — a zero-length or nil ciphertext cannot
// carry a valid GCM nonce+tag, so Rekey must surface a decrypt error rather
// than panicking or silently producing a bogus row. Guards the sweep against a
// NULL/empty secret column slipping through as "rotated".
func TestRekey_EmptyAndNilCiphertextFail(t *testing.T) {
	oldKey, newKey := key32(0x11), key32(0x22)
	for _, tc := range []struct {
		name string
		ct   []byte
	}{
		{"empty", []byte{}},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Rekey(oldKey, newKey, tc.ct); err == nil {
				t.Fatalf("Rekey(%s ciphertext) must fail, got nil error", tc.name)
			}
		})
	}
}

// TestOnNewKey_EmptyAndNilCiphertext — the verify probe must treat an
// undecryptable empty/nil cell as "not on the new key" (false), never true, so
// a NULL secret is not miscounted as already-rotated.
func TestOnNewKey_EmptyAndNilCiphertext(t *testing.T) {
	newKey := key32(0x22)
	if OnNewKey(newKey, []byte{}) {
		t.Fatal("OnNewKey(empty) must be false")
	}
	if OnNewKey(newKey, nil) {
		t.Fatal("OnNewKey(nil) must be false")
	}
}

func TestOnNewKey(t *testing.T) {
	oldKey, newKey := key32(0x11), key32(0x22)
	oldCT, _ := aes.Encrypt([]byte("x"), oldKey)
	newCT, _ := aes.Encrypt([]byte("x"), newKey)

	if OnNewKey(newKey, oldCT) {
		t.Fatal("OnNewKey must be false for a ciphertext encrypted under the old key")
	}
	if !OnNewKey(newKey, newCT) {
		t.Fatal("OnNewKey must be true for a ciphertext encrypted under the new key")
	}
}

func TestParseKeyHex(t *testing.T) {
	valid := hex.EncodeToString(key32(0x33))
	k, err := ParseKeyHex("  " + valid + "\n")
	if err != nil {
		t.Fatalf("ParseKeyHex(valid): %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(k))
	}

	if _, err := ParseKeyHex("not-hex"); err == nil {
		t.Fatal("ParseKeyHex must reject non-hex input")
	}
	if _, err := ParseKeyHex(hex.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("ParseKeyHex must reject a 16-byte key")
	}
}

func TestGenerateKeyHex(t *testing.T) {
	h, err := GenerateKeyHex()
	if err != nil {
		t.Fatalf("GenerateKeyHex: %v", err)
	}
	k, err := ParseKeyHex(h)
	if err != nil {
		t.Fatalf("generated key not parseable: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("generated key wrong length: %d", len(k))
	}
}
