// Package sigstore store_test verifies the in-memory dedup invariant added to
// fix the duplicate-signature-row bug: Store.Add must keep the in-memory cache
// consistent with the DB's UNIQUE(tenant_id, manifest_digest, signer_id) +
// ON CONFLICT DO UPDATE semantics, so a re-sign never leaves two rows for the
// same (tenant, digest, signer).
package sigstore

import (
	"context"
	"testing"
	"time"
)

// rec builds a Record for the shared test tenant + manifest, varying only the
// signer so tests can control the dedup key.
func rec(signerID string) *Record {
	return &Record{
		TenantID:        "tenant-1",
		SignerID:        signerID,
		ManifestDigest:  "sha256:abc",
		RepositoryName:  "org/img",
		SignatureDigest: "sha256:sig-" + signerID,
		KeyID:           signerID,
		SigB64:          "AAAA",
		SignedAt:        time.Now(),
	}
}

// Adding the same (tenant, digest, signer) twice must NOT create a duplicate
// in-memory record — the second Add replaces the first, mirroring the DB's
// ON CONFLICT DO UPDATE behaviour (which keeps the row count at 1).
func TestAdd_DuplicateSigner_Deduplicates(t *testing.T) {
	s := New()
	s.Add(rec("key-1"))
	s.Add(rec("key-1"))

	got := s.List(context.Background(), "tenant-1", "sha256:abc")
	if len(got) != 1 {
		t.Fatalf("List returned %d records, want 1 (duplicate signer must dedup)", len(got))
	}
}

// A re-sign with the same signer should update the stored signature digest in
// place rather than leaving the stale one behind.
func TestAdd_DuplicateSigner_UpdatesInPlace(t *testing.T) {
	s := New()
	first := rec("key-1")
	first.SignatureDigest = "sha256:old"
	s.Add(first)

	second := rec("key-1")
	second.SignatureDigest = "sha256:new"
	s.Add(second)

	got := s.List(context.Background(), "tenant-1", "sha256:abc")
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	if got[0].SignatureDigest != "sha256:new" {
		t.Errorf("SignatureDigest = %q, want sha256:new (re-sign should update in place)", got[0].SignatureDigest)
	}
}

// Distinct signers on the same manifest are all retained — dedup keys on
// signer_id, not just (tenant, digest).
func TestAdd_DistinctSigners_AllRetained(t *testing.T) {
	s := New()
	s.Add(rec("key-1"))
	s.Add(rec("key-2"))
	s.Add(rec("key-3"))

	got := s.List(context.Background(), "tenant-1", "sha256:abc")
	if len(got) != 3 {
		t.Fatalf("want 3 distinct-signer records, got %d", len(got))
	}
}
