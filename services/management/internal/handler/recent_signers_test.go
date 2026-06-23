// recent_signers_test.go — tests for the BFF-orchestrated recent-signers
// picker source that powers the Approve-Trusted-Key dialog's "Pick from
// recent signers" mode (futures.md Tier 1 #3 follow-up, 2026-06-23).
//
// We reuse the signerTestEnv harness from signature_test.go because the
// route depends on both the metadata and signer fakes — the env wires
// both into a real handler.New() and serves over httptest. The fakes'
// canned behavior is enough for these tests since the dedupe + sort
// logic is pure aggregation on top of whatever the signer returns.
package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
)

// recentSignersWire mirrors the JSON wire shape on the FE side. Defined
// here (not imported) because the handler's recentSignersResponse +
// recentSignerEntry types are unexported.
type recentSignersWire struct {
	Signers []struct {
		KeyID        string    `json:"key_id"`
		SignerID     string    `json:"signer_id,omitempty"`
		LastSignedAt time.Time `json:"last_signed_at"`
		TagCount     int       `json:"tag_count"`
	} `json:"signers"`
}

// TestRecentSigners_emptySigner_returns200WithEmptyList verifies the
// degraded-signer path: when SIGNER_GRPC_ADDR is unset (signer client nil)
// the route returns 200 + empty list so the FE picker falls back to
// Manual entry without an error toast. We can't directly test the nil-
// signer path through signerTestEnv (it always wires one) — that path is
// exercised by handler.go's nil-guard, asserted indirectly by code
// review. This test exercises the populated-signer happy path instead.
func TestRecentSigners_populatedSigner_dedupesByKeyID(t *testing.T) {
	env := newSignerTestEnv(t)
	// fakeMetaServer.ListTags streams a single tag (v1.0, sha256:abc123).
	// Configure the fake signer to return two signatures with the SAME
	// key_id from two different signer_ids — the dedup logic should
	// collapse them into one entry keyed by key_id with the first
	// non-empty signer_id and the latest signed_at preserved.
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	env.signer.signatures = []*signerv1.Signature{
		{SignerId: "ci-bot-A", KeyId: "key-deadbeef", SignatureDigest: "sha256:s1", SignedAt: timestamppb.New(earlier)},
		{SignerId: "ci-bot-B", KeyId: "key-deadbeef", SignatureDigest: "sha256:s2", SignedAt: timestamppb.New(now)},
		{SignerId: "ci-other", KeyId: "key-cafe", SignatureDigest: "sha256:s3", SignedAt: timestamppb.New(now)},
	}

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/recent-signers", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body recentSignersWire
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Two distinct key_ids → two entries even though the fake returned
	// three signature rows. Same-key_id rows must collapse.
	if got := len(body.Signers); got != 2 {
		t.Fatalf("expected 2 deduped entries, got %d: %+v", got, body.Signers)
	}

	// Both entries should resolve a non-empty signer_id (first seen wins).
	for _, s := range body.Signers {
		if s.SignerID == "" {
			t.Errorf("entry %q missing signer_id auto-fill", s.KeyID)
		}
		if s.TagCount < 1 {
			t.Errorf("entry %q has tag_count=%d, expected >=1", s.KeyID, s.TagCount)
		}
	}
}

// TestRecentSigners_emptyResponseShape verifies the route returns a
// non-null `signers` slice when the signer has no data — the FE branches
// on `signers.length === 0` and a `null` body would NPE the dropdown.
func TestRecentSigners_unsignedRepo_returnsEmptySlice(t *testing.T) {
	env := newSignerTestEnv(t)
	// No signatures configured → fakeSignerServer.signatures still has
	// the default canned single-row response. Override to an empty slice
	// to simulate an unsigned repo.
	env.signer.signatures = []*signerv1.Signature{}

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/recent-signers", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read the raw body and confirm `signers` is an array, not null.
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	signers, ok := raw["signers"].([]any)
	if !ok {
		t.Fatalf("expected signers array, got %T: %v", raw["signers"], raw["signers"])
	}
	if len(signers) != 0 {
		t.Errorf("expected empty signers, got %d", len(signers))
	}
}

// TestRecentSigners_invalidLimit_returns400 covers the input-validation
// guard. ?limit=abc should fail fast rather than silently coercing.
func TestRecentSigners_invalidLimit_returns400(t *testing.T) {
	env := newSignerTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/recent-signers?limit=abc", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRecentSigners_signerlessHandler_returnsEmptyList confirms the
// nil-signer fallback: when SIGNER_GRPC_ADDR is unset the route emits an
// empty list with 200 so the dialog degrades gracefully. Uses
// newSignerlessTestEnv from signature_test.go which deliberately omits
// the signer wiring.
func TestRecentSigners_signerlessHandler_returnsEmptyList(t *testing.T) {
	srv := newSignerlessTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/repositories/myorg/myrepo/recent-signers", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body recentSignersWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Signers) != 0 {
		t.Errorf("expected empty list from signerless handler, got %+v", body.Signers)
	}
}
