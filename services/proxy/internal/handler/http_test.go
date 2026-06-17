// Package handler_test provides unit tests for the pull-through cache HTTP handler
// pure helper functions that do not require network or database access.
package handler

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── blobKey tests ─────────────────────────────────────────────────────────────

func TestBlobKey_ValidDigest_ProducesExpectedKey(t *testing.T) {
	tenantID := "tenant-abc-123"
	digest := "sha256:aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	got := blobKey(tenantID, digest)

	// Expected format: blobs/<tenantID>/sha256/<first2>/<digest-without-prefix>
	want := "blobs/tenant-abc-123/sha256/aa/aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	if got != want {
		t.Errorf("blobKey = %q, want %q", got, want)
	}
}

func TestBlobKey_ContainsTenantPrefix(t *testing.T) {
	tenantID := "my-tenant"
	digest := "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	got := blobKey(tenantID, digest)
	prefix := "blobs/" + tenantID
	if len(got) < len(prefix) {
		t.Fatalf("blob key too short: %q", got)
	}
	if got[:len(prefix)] != prefix {
		t.Errorf("expected key to start with %q, got %q", prefix, got[:len(prefix)])
	}
}

func TestBlobKey_DirectoryShardingUses2Chars(t *testing.T) {
	tenantID := "t"
	digest := "sha256:ff11223344556677889900aabbccddeeff11223344556677889900aabbccddee"
	got := blobKey(tenantID, digest)
	// The shard prefix (2-char directory) should be "ff"
	shardOffset := len("blobs/t/sha256/")
	shard := got[shardOffset : shardOffset+2]
	if shard != "ff" {
		t.Errorf("expected shard %q in key %q, got %q", "ff", got, shard)
	}
}

// ── hexToKey tests ────────────────────────────────────────────────────────────

func TestHexToKey_Valid32ByteHex_ReturnsKey(t *testing.T) {
	// 64 hex chars = 32 bytes
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	key, err := hexToKey(hexKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
}

func TestHexToKey_InvalidHex_ReturnsError(t *testing.T) {
	_, err := hexToKey("gggg")
	if err == nil {
		t.Error("expected error for invalid hex string")
	}
}

func TestHexToKey_WrongLength_ReturnsError(t *testing.T) {
	// 16 bytes (32 hex chars) — too short
	_, err := hexToKey("0102030405060708090a0b0c0d0e0f10")
	if err == nil {
		t.Error("expected error for non-32-byte key")
	}
}

func TestHexToKey_EmptyString_ReturnsError(t *testing.T) {
	_, err := hexToKey("")
	if err == nil {
		t.Error("expected error for empty key")
	}
}

// ── digestRE tests ────────────────────────────────────────────────────────────

func TestDigestRE_ValidSHA256_Matches(t *testing.T) {
	valid := []string{
		"sha256:aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
		"sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	for _, d := range valid {
		if !digestRE.MatchString(d) {
			t.Errorf("digestRE did not match valid digest %q", d)
		}
	}
}

func TestDigestRE_InvalidDigests_DoNotMatch(t *testing.T) {
	invalid := []string{
		"sha256:UPPER",    // uppercase hex
		"md5:aabbccdd",   // wrong algorithm
		"sha256:short",   // too short
		"sha256:",        // empty hex part
		"",               // empty
		"aabbccddeeff",   // missing prefix
	}
	for _, d := range invalid {
		if digestRE.MatchString(d) {
			t.Errorf("digestRE unexpectedly matched invalid digest %q", d)
		}
	}
}

// ── isGRPCNotFound tests ──────────────────────────────────────────────────────

func TestIsGRPCNotFound_NotFoundCode_ReturnsTrue(t *testing.T) {
	err := status.Error(codes.NotFound, "not found")
	if !isGRPCNotFound(err) {
		t.Error("expected isGRPCNotFound=true for NotFound status")
	}
}

func TestIsGRPCNotFound_OtherCode_ReturnsFalse(t *testing.T) {
	err := status.Error(codes.Internal, "internal error")
	if isGRPCNotFound(err) {
		t.Error("expected isGRPCNotFound=false for Internal status")
	}
}

func TestIsGRPCNotFound_PlainError_ReturnsFalse(t *testing.T) {
	err := errors.New("plain error")
	if isGRPCNotFound(err) {
		t.Error("expected isGRPCNotFound=false for plain error")
	}
}
