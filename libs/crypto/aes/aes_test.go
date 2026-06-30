package aes

import (
	"bytes"
	stdaes "crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"testing"
)

// encryptLegacy produces a pre-v1 ciphertext (nonce || ct || tag) so the
// back-compat path of Decrypt can be tested without resurrecting the old
// Encrypt implementation. Mirrors the package's pre-Phase-6.4 logic exactly.
func encryptLegacy(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	block, err := stdaes.NewCipher(key)
	if err != nil {
		t.Fatalf("legacy cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("legacy gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("legacy nonce: %v", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil)
}

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

// TestEncrypt_HasVersionPrefix verifies the new layout: ciphertext produced
// by Encrypt starts with Version (0x01) and is exactly 1 byte longer than
// the legacy layout for the same plaintext (extra version byte).
func TestEncrypt_HasVersionPrefix(t *testing.T) {
	pt := []byte("hello kek")
	ct, err := Encrypt(pt, testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct[0] != Version {
		t.Errorf("expected leading byte = Version (%#x), got %#x", Version, ct[0])
	}
	// Layout: version(1) + nonce(12) + ct(len(pt)) + tag(16) = 29 + len(pt).
	wantLen := 1 + 12 + len(pt) + 16
	if len(ct) != wantLen {
		t.Errorf("expected ciphertext length %d, got %d", wantLen, len(ct))
	}
}

// TestDecrypt_LegacyLayout verifies pre-v1 rows still decrypt under the
// fallback path. This is the core back-compat guarantee for Phase 6.4 —
// any row written before the version prefix landed must keep working.
func TestDecrypt_LegacyLayout(t *testing.T) {
	plaintext := []byte("legacy oauth client secret")
	legacyCT := encryptLegacy(t, plaintext, testKey)
	got, err := Decrypt(legacyCT, testKey)
	if err != nil {
		t.Fatalf("Decrypt legacy layout: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("legacy round-trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestDecrypt_LegacyWithVersionByteCollision exercises the ~1/256 case where
// a legacy nonce happens to begin with 0x01. The v1 codec should fail GCM
// auth (because it'll mis-slice the nonce), and the legacy fallback should
// then succeed. We force the collision by patching the first byte of a
// freshly-generated legacy nonce to 0x01 *before* encryption.
func TestDecrypt_LegacyWithVersionByteCollision(t *testing.T) {
	plaintext := []byte("collision case")
	block, err := stdaes.NewCipher(testKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	// Build a legacy ciphertext where the first nonce byte == Version. This
	// is what trips the "v1 dispatch then fall through to legacy" path.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	nonce[0] = Version
	legacyCT := gcm.Seal(nonce, nonce, plaintext, nil)
	if legacyCT[0] != Version {
		t.Fatalf("test setup: expected leading byte = Version, got %#x", legacyCT[0])
	}

	got, err := Decrypt(legacyCT, testKey)
	if err != nil {
		t.Fatalf("Decrypt with version-byte collision: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("collision round-trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestDecrypt_TamperedVersionByte verifies that flipping the version byte
// on a v1 row makes Decrypt fail. The flipped byte (0x02) is not a known
// version, so dispatch goes straight to legacy; legacy parsing reads the
// wrong bytes as nonce/ct and GCM auth rejects them. We assert the failure
// is a real error, not silent corruption.
func TestDecrypt_TamperedVersionByte(t *testing.T) {
	ct, err := Encrypt([]byte("v1 row"), testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct[0] = 0x02 // not a known version → legacy fallback → GCM auth fails
	if _, err := Decrypt(ct, testKey); err == nil {
		t.Error("expected error when v1 version byte is tampered with")
	}
}

// TestDecrypt_TamperedV1Body verifies tampering inside the v1 ciphertext
// body fails GCM auth on both the v1 path AND the legacy fallback — no
// silent downgrade. Flips a byte mid-ciphertext (not the version, not the
// trailing tag) so we exercise the v1-fails-then-legacy-also-fails branch.
func TestDecrypt_TamperedV1Body(t *testing.T) {
	ct, err := Encrypt([]byte("v1 row body tamper"), testKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Flip a byte well inside the ct body — past version+nonce, before tag.
	mid := 1 + 12 + 3
	ct[mid] ^= 0xff
	if _, err := Decrypt(ct, testKey); err == nil {
		t.Error("expected error when v1 ciphertext body is tampered with")
	}
}

// TestVersionConstant pins the current version byte. Bumping Version is a
// deliberate, reviewed event (paired Decrypt branch required) — this test
// is a tripwire so an accidental edit shows up in CI.
func TestVersionConstant(t *testing.T) {
	if Version != 0x01 {
		t.Errorf("Version constant changed unexpectedly: got %#x, want 0x01", Version)
	}
}
