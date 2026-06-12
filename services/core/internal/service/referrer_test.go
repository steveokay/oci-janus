// Package service_test exercises the ReferrerStore using an in-process Redis
// (miniredis). No real Redis server is required.
package service

import (
	"context"
	"testing"
)

// TestReferrerKey_format verifies the Redis key layout for the referrer store.
// Key layout: refs:<tenantID>:<repoName>:<subjectDigest>
func TestReferrerKey_format(t *testing.T) {
	got := referrerKey("tenant-1", "myorg/myrepo", "sha256:abcdef")
	const want = "refs:tenant-1:myorg/myrepo:sha256:abcdef"
	if got != want {
		t.Errorf("referrerKey = %q, want %q", got, want)
	}
}

// TestReferrerStore_addAndList verifies that a stored descriptor is returned by List.
func TestReferrerStore_addAndList(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	ctx := context.Background()

	desc := ReferrerDescriptor{
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		Digest:       "sha256:referrer1",
		Size:         512,
		ArtifactType: "application/vnd.example.sbom",
		Annotations:  map[string]string{"org.opencontainers.annotation": "value"},
	}

	if err := store.Add(ctx, "tenant-1", "myorg/myimage", "sha256:subject1", desc); err != nil {
		t.Fatalf("Add: %v", err)
	}

	descs, err := store.List(ctx, "tenant-1", "myorg/myimage", "sha256:subject1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(descs) != 1 {
		t.Fatalf("expected 1 descriptor, got %d", len(descs))
	}
	if descs[0].Digest != desc.Digest {
		t.Errorf("Digest: got %q, want %q", descs[0].Digest, desc.Digest)
	}
	if descs[0].ArtifactType != desc.ArtifactType {
		t.Errorf("ArtifactType: got %q, want %q", descs[0].ArtifactType, desc.ArtifactType)
	}
}

// TestReferrerStore_listEmpty verifies that listing referrers for an unknown
// subject digest returns an empty (not nil) slice without error.
func TestReferrerStore_listEmpty(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	descs, err := store.List(context.Background(), "tenant-1", "org/repo", "sha256:nosuchreferrer")
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if descs == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(descs) != 0 {
		t.Errorf("expected 0 descriptors, got %d", len(descs))
	}
}

// TestReferrerStore_multipleDescriptors verifies that multiple Add calls for the
// same subject accumulate all descriptors and List returns them all.
func TestReferrerStore_multipleDescriptors(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	ctx := context.Background()

	const (
		tenantID   = "tenant-2"
		repoName   = "org/myrepo"
		subjectDig = "sha256:mainimage"
	)

	// Add three distinct referrers (e.g. SBOM, signature, attestation).
	for i, digest := range []string{"sha256:ref1", "sha256:ref2", "sha256:ref3"} {
		desc := ReferrerDescriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    digest,
			Size:      int64(i + 1),
		}
		if err := store.Add(ctx, tenantID, repoName, subjectDig, desc); err != nil {
			t.Fatalf("Add %s: %v", digest, err)
		}
	}

	descs, err := store.List(ctx, tenantID, repoName, subjectDig)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(descs) != 3 {
		t.Errorf("expected 3 descriptors, got %d", len(descs))
	}
}

// TestReferrerStore_isolatedByTenant confirms that referrers stored for one
// tenant are not visible when querying a different tenant.
func TestReferrerStore_isolatedByTenant(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	ctx := context.Background()

	desc := ReferrerDescriptor{Digest: "sha256:ref-secret", MediaType: "application/json"}
	if err := store.Add(ctx, "tenant-A", "org/repo", "sha256:subject", desc); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Query from a different tenant must return empty list.
	descs, err := store.List(ctx, "tenant-B", "org/repo", "sha256:subject")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(descs) != 0 {
		t.Errorf("tenant isolation breach: expected 0 descriptors for tenant-B, got %d", len(descs))
	}
}

// TestGetReferrers_filterByArtifactType verifies that GetReferrers correctly
// filters the list when artifactType is non-empty and sets filtered=true.
// This test calls GetReferrers via a Registry with a fake referrer store.
func TestGetReferrers_filterByArtifactType(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	ctx := context.Background()

	// Populate two descriptors with different artifact types.
	sbom := ReferrerDescriptor{
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		Digest:       "sha256:sbom",
		Size:         100,
		ArtifactType: "application/vnd.example.sbom",
	}
	sig := ReferrerDescriptor{
		MediaType:    "application/vnd.oci.image.manifest.v1+json",
		Digest:       "sha256:sig",
		Size:         50,
		ArtifactType: "application/vnd.example.signature",
	}

	for _, d := range []ReferrerDescriptor{sbom, sig} {
		if err := store.Add(ctx, "tenant-1", "org/repo", "sha256:subject", d); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	// Build a minimal Registry that only has a referrer store.
	r := &Registry{referrers: store}

	// Filter to sbom only.
	descs, filtered, err := r.GetReferrers(ctx, "tenant-1", "org/repo", "sha256:subject", "application/vnd.example.sbom")
	if err != nil {
		t.Fatalf("GetReferrers: %v", err)
	}
	if !filtered {
		t.Error("expected filtered=true when artifactType is provided")
	}
	if len(descs) != 1 {
		t.Fatalf("expected 1 descriptor after filter, got %d", len(descs))
	}
	if descs[0].Digest != "sha256:sbom" {
		t.Errorf("filtered descriptor digest: got %q, want sha256:sbom", descs[0].Digest)
	}
}

// TestGetReferrers_noFilter verifies that GetReferrers with an empty artifactType
// returns all descriptors and sets filtered=false.
func TestGetReferrers_noFilter(t *testing.T) {
	rdb, cleanup := newTestRedis(t)
	defer cleanup()

	store := NewReferrerStore(rdb)
	ctx := context.Background()

	for _, d := range []ReferrerDescriptor{
		{Digest: "sha256:a", MediaType: "application/json", ArtifactType: "type.a"},
		{Digest: "sha256:b", MediaType: "application/json", ArtifactType: "type.b"},
	} {
		if err := store.Add(ctx, "t1", "o/r", "sha256:sub", d); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	r := &Registry{referrers: store}
	descs, filtered, err := r.GetReferrers(ctx, "t1", "o/r", "sha256:sub", "")
	if err != nil {
		t.Fatalf("GetReferrers: %v", err)
	}
	if filtered {
		t.Error("expected filtered=false when no artifactType is provided")
	}
	if len(descs) != 2 {
		t.Errorf("expected 2 descriptors, got %d", len(descs))
	}
}
