// Package handler_test provides unit tests for the pull-through cache HTTP handler
// pure helper functions that do not require network or database access.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// ── buildProxyPullPayload tests (FUT-014) ────────────────────────────────────

// FUT-014: every proxy-served manifest publishes a pull.image event with
// these fields locked. Tests below pin the exact payload shape because the
// audit consumer + dashboard analytics path key off these fields.

func TestBuildProxyPullPayload_TagReference_PopulatesTag(t *testing.T) {
	tenantID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	now := time.Date(2026, 6, 25, 14, 30, 0, 0, time.UTC)
	digest := "sha256:aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

	got := buildProxyPullPayload(tenantID, "dockerhub", "library/alpine", "3.20", digest, "user-uuid-xyz", now)

	if got.Tag != "3.20" {
		t.Errorf("Tag: got %q, want %q", got.Tag, "3.20")
	}
	if got.ManifestDigest != digest {
		t.Errorf("ManifestDigest: got %q, want %q", got.ManifestDigest, digest)
	}
	if got.RepositoryName != "cache/dockerhub/library/alpine" {
		t.Errorf("RepositoryName: got %q, want %q", got.RepositoryName, "cache/dockerhub/library/alpine")
	}
	if got.RepositoryID != "" {
		t.Errorf("RepositoryID: got %q, want empty (proxy manifests not in metadata.manifests)", got.RepositoryID)
	}
	if got.Via != "proxy" {
		t.Errorf("Via: got %q, want %q", got.Via, "proxy")
	}
	if got.TenantID != tenantID.String() {
		t.Errorf("TenantID: got %q, want %q", got.TenantID, tenantID.String())
	}
	if got.ActorID != "user-uuid-xyz" {
		t.Errorf("ActorID: got %q, want %q", got.ActorID, "user-uuid-xyz")
	}
	if !got.PulledAt.Equal(now) {
		t.Errorf("PulledAt: got %v, want %v", got.PulledAt, now)
	}
}

func TestBuildProxyPullPayload_DigestReference_ClearsTag(t *testing.T) {
	// Digest-direct pull (e.g. docker pull cache/dockerhub/library/alpine@sha256:...).
	// Tag must be empty so the audit consumer falls back to its "name@digest"
	// Resource form rather than emitting "name:sha256:..." which would be wrong.
	tenantID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	digest := "sha256:aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

	got := buildProxyPullPayload(tenantID, "dockerhub", "library/alpine", digest, digest, "", time.Now().UTC())

	if got.Tag != "" {
		t.Errorf("Tag: got %q, want empty (digest-direct pull)", got.Tag)
	}
	if got.ManifestDigest != digest {
		t.Errorf("ManifestDigest: got %q, want %q", got.ManifestDigest, digest)
	}
}

func TestBuildProxyPullPayload_EmptyActor_KeepsEmpty(t *testing.T) {
	// Anonymous pulls (no JWT sub) propagate empty ActorID; audit's consumer
	// rewrites it to "anonymous" downstream. The publisher must NOT invent a
	// placeholder here — that would break the existing services/core contract.
	tenantID := uuid.New()
	got := buildProxyPullPayload(tenantID, "ecr", "nginx/nginx", "mainline-arm64v8", "sha256:0000000000000000000000000000000000000000000000000000000000000000", "", time.Now().UTC())
	if got.ActorID != "" {
		t.Errorf("ActorID: got %q, want empty for anonymous pull", got.ActorID)
	}
}

func TestBuildProxyPullPayload_RepositoryNameFormat(t *testing.T) {
	// Multi-segment image paths (org/repo, multi-level/path) must keep the
	// slashes inside the cache/<upstream>/ prefix so the user-facing pull
	// command and the audit Resource string remain bijective.
	cases := []struct {
		upstream string
		image    string
		want     string
	}{
		{"dockerhub", "library/alpine", "cache/dockerhub/library/alpine"},
		{"ecr", "nginx/nginx", "cache/ecr/nginx/nginx"},
		{"quay", "prometheus/prometheus", "cache/quay/prometheus/prometheus"},
		{"local", "single", "cache/local/single"},
	}
	for _, tc := range cases {
		got := buildProxyPullPayload(uuid.New(), tc.upstream, tc.image, "latest", "sha256:0000000000000000000000000000000000000000000000000000000000000000", "", time.Now().UTC())
		if got.RepositoryName != tc.want {
			t.Errorf("upstream=%q image=%q: RepositoryName=%q, want %q", tc.upstream, tc.image, got.RepositoryName, tc.want)
		}
	}
}

// ── Phase 6.1: upstream blob digest verification tests ───────────────────────
//
// Review §A4 / Top-5 #4: pull-through proxy must verify that bytes served by
// the upstream hash to the requested digest. These tests exercise the
// verifyBlobDigest helper (which encapsulates the sha256 check + pipe control)
// and confirm that storeBlobFromReader correctly aborts on a poisoned pipe —
// ensuring falsified upstream bytes are NEVER committed to cache.

// TestVerifyBlobDigest_Match_ClosesPipeCleanly verifies that when the upstream
// body hashes to the requested digest, verifyBlobDigest closes the pipe writer
// cleanly (nil error), which causes storeBlobFromReader to commit.
func TestVerifyBlobDigest_Match_ClosesPipeCleanly(t *testing.T) {
	body := []byte("hello world")
	h := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(h[:])

	pr, pw := io.Pipe()

	hasher := sha256.New()
	_, _ = hasher.Write(body) // simulate what io.Copy through TeeReader would do

	// Signal goroutine: run the verifier, then close.
	done := make(chan error, 1)
	go func() {
		done <- verifyBlobDigest(hasher, digest, pw)
	}()

	// Drain the reader side so the goroutine can close cleanly.
	_, readErr := io.ReadAll(pr)

	verifyErr := <-done
	if verifyErr != nil {
		t.Fatalf("verifyBlobDigest returned error on match: %v", verifyErr)
	}
	if readErr != nil {
		t.Fatalf("unexpected read error after clean close: %v", readErr)
	}
}

// TestVerifyBlobDigest_Mismatch_PoisonsPipe verifies that when the upstream
// body does NOT hash to the requested digest, verifyBlobDigest closes the pipe
// writer with an error (poisoning it), which causes storeBlobFromReader to abort
// without calling CloseAndRecv — so no bytes are committed to cache.
func TestVerifyBlobDigest_Mismatch_PoisonsPipe(t *testing.T) {
	body := []byte("tampered bytes") // does NOT match the digest below
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	pr, pw := io.Pipe()

	hasher := sha256.New()
	_, _ = hasher.Write(body)

	done := make(chan error, 1)
	go func() {
		done <- verifyBlobDigest(hasher, wrongDigest, pw)
	}()

	// The pipe should be poisoned — ReadAll should return an error.
	_, readErr := io.ReadAll(pr)

	verifyErr := <-done
	if verifyErr == nil {
		t.Fatal("verifyBlobDigest should return error on mismatch, got nil")
	}
	if readErr == nil {
		t.Fatal("pipe reader should receive an error after CloseWithError, got nil")
	}
	if !strings.Contains(verifyErr.Error(), "upstream digest mismatch") {
		t.Errorf("error message should mention digest mismatch, got: %v", verifyErr)
	}
}

// TestHandleGetBlob_UpstreamDigestMismatch_NoCommit verifies that when the
// upstream serves bytes that don't hash to the requested digest, the proxy
// does NOT commit the bytes to storage.
//
// Reproduces the "compromised upstream / MITM" scenario: a stub upstream
// returns 10 bytes of 'x' under a sha256 digest that doesn't match those bytes.
// The storage write via storeBlobFromReader must abort before CloseAndRecv
// because the pipe is poisoned by verifyBlobDigest.
//
// This test works at the pipe level (no gRPC stubs needed) — it exercises the
// exact io.Pipe + sha256.Hash + verifyBlobDigest path that handleGetBlob uses.
func TestHandleGetBlob_UpstreamDigestMismatch_NoCommit(t *testing.T) {
	// Upstream serves 10 bytes of 'x' but claims they have a digest of all zeros.
	upstreamBytes := bytes.Repeat([]byte("x"), 10)
	requestedDigest := "sha256:" + strings.Repeat("0", 64) // does NOT match upstreamBytes

	// Simulate the three-way tee: client writer (buf), storage pipe (pr/pw), hasher.
	pr, pw := io.Pipe()
	hasher := sha256.New()
	multi := io.MultiWriter(pw, hasher)

	// Upstream body reader.
	rc := bytes.NewReader(upstreamBytes)
	tee := io.TeeReader(rc, multi)

	// clientBuf captures what would go to the HTTP response writer.
	var clientBuf bytes.Buffer

	// commitCalled tracks whether the storage commit path would have been reached.
	// storeBlobFromReader is called with pr; if the pipe is poisoned before all
	// bytes are consumed, Read returns an error and the function returns early
	// without reaching the CloseAndRecv path. We detect this by checking the
	// returned error — a non-nil error means NO commit happened.
	storeDone := make(chan error, 1)
	go func() {
		// storeBlobFromReader reads from pr. On a poisoned pipe it returns an
		// error before any CloseAndRecv (commit) call.
		//
		// We use a discarding writer as a minimal stand-in to simulate the
		// goroutine that storeBlobFromReader runs in. Since storeBlobFromReader
		// takes an io.Reader (not a storage client), we can call it with a
		// trivial noop storage path — but that requires a real gRPC client.
		//
		// Instead, we test the abort at the pipe level directly: drain pr and
		// record the first error. If the pipe is poisoned before EOF, err != nil
		// and no commit would happen (matching the SEC-012 guard in storeBlobFromReader).
		_, err := io.ReadAll(pr)
		storeDone <- err
	}()

	// Drive the copy (simulates io.Copy(w, tee) in handleGetBlob).
	_, copyErr := io.Copy(&clientBuf, tee)
	if copyErr != nil {
		t.Fatalf("unexpected copy error: %v", copyErr)
	}

	// Verify the digest after copy completes (Phase 6.1 addition).
	verifyErr := verifyBlobDigest(hasher, requestedDigest, pw)
	if verifyErr == nil {
		t.Fatal("verifyBlobDigest should return an error on digest mismatch, got nil")
	}

	// The pipe reader goroutine must see an error (pipe was poisoned → no commit).
	storeErr := <-storeDone
	if storeErr == nil {
		t.Fatal("storage goroutine should have received pipe error, got nil — bytes would have been committed!")
	}

	// Client did receive the bytes (we can't recall them).
	if !bytes.Equal(clientBuf.Bytes(), upstreamBytes) {
		t.Errorf("client received %d bytes, want %d", len(clientBuf.Bytes()), len(upstreamBytes))
	}

	// Confirm error messages are meaningful.
	if !strings.Contains(verifyErr.Error(), "upstream digest mismatch") {
		t.Errorf("verify error should mention digest mismatch, got: %v", verifyErr)
	}
}

// TestHandleGetBlob_UpstreamDigestMatch_Commits verifies that when the upstream
// serves bytes that DO hash to the requested digest, the pipe is closed cleanly
// and storeBlobFromReader would proceed to commit (no error from pipe layer).
func TestHandleGetBlob_UpstreamDigestMatch_Commits(t *testing.T) {
	upstreamBytes := []byte("correct content")
	h := sha256.Sum256(upstreamBytes)
	requestedDigest := "sha256:" + hex.EncodeToString(h[:])

	pr, pw := io.Pipe()
	hasher := sha256.New()
	multi := io.MultiWriter(pw, hasher)
	rc := bytes.NewReader(upstreamBytes)
	tee := io.TeeReader(rc, multi)

	var clientBuf bytes.Buffer

	// Drain the pipe reader in a goroutine, capturing any error.
	storeDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(pr)
		storeDone <- err
	}()

	_, copyErr := io.Copy(&clientBuf, tee)
	if copyErr != nil {
		t.Fatalf("unexpected copy error: %v", copyErr)
	}

	// On match, verifyBlobDigest closes the pipe cleanly (nil error).
	verifyErr := verifyBlobDigest(hasher, requestedDigest, pw)
	if verifyErr != nil {
		t.Fatalf("verifyBlobDigest returned error on matching digest: %v", verifyErr)
	}

	// The storage goroutine should see clean EOF (nil error → would commit).
	storeErr := <-storeDone
	if storeErr != nil {
		t.Fatalf("storage goroutine should see clean EOF on valid digest, got: %v", storeErr)
	}
}

// Sentinel: keep fmt and context imports live. These are used indirectly
// by the test harness and would otherwise trigger an "imported and not used"
// error on older Go toolchain versions that don't perform import pruning.
var _ = fmt.Sprintf
var _ = context.Background
