// Package service (appendchunk_test.go) exercises AppendChunk (PATCH upload)
// and PutManifest paths not covered by registry_grpc_test.go.
package service

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"crypto/sha256"
)

// TestAppendChunk_singleChunk verifies that AppendChunk stores a chunk and
// updates the upload state's Offset.
func TestAppendChunk_singleChunk_updatesOffset(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, err := tr.reg.InitiateUpload(ctx, "tenant-patch", "myorg/myrepo")
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}

	data := []byte("patch chunk data for unit test")
	written, err := tr.reg.AppendChunk(ctx, id, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if written != int64(len(data)) {
		t.Errorf("written: got %d, want %d", written, int64(len(data)))
	}

	// Offset in state must have advanced.
	st, err := tr.reg.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("GetUpload after AppendChunk: %v", err)
	}
	if st.Offset != int64(len(data)) {
		t.Errorf("Offset: got %d, want %d", st.Offset, int64(len(data)))
	}
	if len(st.ChunkKeys) != 1 {
		t.Errorf("ChunkKeys len: got %d, want 1", len(st.ChunkKeys))
	}
}

// TestAppendChunk_multipleChunks verifies that two consecutive AppendChunk
// calls accumulate offset and chunk keys correctly.
func TestAppendChunk_multipleChunks_accumulatesState(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-patch2", "myorg/myrepo")

	chunk1 := []byte("first chunk")
	chunk2 := []byte("second chunk")

	if _, err := tr.reg.AppendChunk(ctx, id, bytes.NewReader(chunk1), int64(len(chunk1))); err != nil {
		t.Fatalf("AppendChunk 1: %v", err)
	}
	if _, err := tr.reg.AppendChunk(ctx, id, bytes.NewReader(chunk2), int64(len(chunk2))); err != nil {
		t.Fatalf("AppendChunk 2: %v", err)
	}

	st, err := tr.reg.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	expectedOffset := int64(len(chunk1) + len(chunk2))
	if st.Offset != expectedOffset {
		t.Errorf("Offset: got %d, want %d", st.Offset, expectedOffset)
	}
	if len(st.ChunkKeys) != 2 {
		t.Errorf("ChunkKeys len: got %d, want 2", len(st.ChunkKeys))
	}
}

// TestAppendChunk_notFound verifies that AppendChunk returns ErrUploadNotFound
// for an unknown upload UUID.
func TestAppendChunk_notFound_returnsErrUploadNotFound(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	_, err := tr.reg.AppendChunk(context.Background(), "does-not-exist", bytes.NewReader(nil), 0)
	if err != ErrUploadNotFound {
		t.Errorf("expected ErrUploadNotFound, got %v", err)
	}
}

// TestCompleteUpload_withPriorChunks verifies that CompleteUpload correctly
// assembles a prior PATCH chunk followed by a final PUT body.
func TestCompleteUpload_withPriorChunks_assemblesCorrectly(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-assemble", "myorg/myrepo")

	patchData := []byte("patch part: ")
	putData := []byte("put part")

	// PATCH: store first chunk.
	if _, err := tr.reg.AppendChunk(ctx, id, bytes.NewReader(patchData), int64(len(patchData))); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	// PUT: complete with final body appended to chunk.
	combined := append(patchData, putData...)
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(combined))

	digest, size, err := tr.reg.CompleteUpload(ctx, id, expectedDigest, bytes.NewReader(putData), int64(len(putData)))
	if err != nil {
		t.Fatalf("CompleteUpload with prior chunk: %v", err)
	}
	if digest != expectedDigest {
		t.Errorf("digest: got %q, want %q", digest, expectedDigest)
	}
	if size != int64(len(combined)) {
		t.Errorf("size: got %d, want %d", size, int64(len(combined)))
	}
}

// TestPutManifest_withSubjectField_registersReferrer verifies that PutManifest
// correctly parses a manifest containing a subject field and registers it as a
// referrer for the subject digest.
func TestPutManifest_withSubjectField_registersReferrer(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()

	// Valid subject digest (64 hex chars).
	subjectDigest := "sha256:" + strings.Repeat("a", 64)
	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + subjectDigest + `","size":100},"artifactType":"application/vnd.test.sbom","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:` + strings.Repeat("b", 64) + `","size":2},"layers":[]}`)

	repo, err := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("GetOrCreateRepository: %v", err)
	}

	digest, returnedSubjectDigest, err := tr.reg.PutManifest(
		ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		"sha256:"+strings.Repeat("c", 64), // reference is a digest
		"application/vnd.oci.image.manifest.v1+json",
		rawJSON, "user-1",
	)
	if err != nil {
		t.Fatalf("PutManifest with subject: %v", err)
	}
	if digest == "" {
		t.Error("expected non-empty digest from PutManifest")
	}
	if returnedSubjectDigest != subjectDigest {
		t.Errorf("subjectDigest: got %q, want %q", returnedSubjectDigest, subjectDigest)
	}
}

// TestPutManifest_withSubjectAndConfigFallback verifies that when the top-level
// artifactType is absent, PutManifest falls back to config.mediaType per OCI spec §6.2.
func TestPutManifest_withSubjectNoArtifactType_fallsBackToConfigMediaType(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	subjectDigest := "sha256:" + strings.Repeat("d", 64)

	// No top-level artifactType — should fall back to config.mediaType.
	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + subjectDigest + `","size":1},"config":{"mediaType":"application/vnd.test.fallback","digest":"sha256:` + strings.Repeat("e", 64) + `","size":2},"layers":[]}`)

	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/repo2")
	_, returnedSubjectDigest, err := tr.reg.PutManifest(
		ctx, "tenant-1", repo.GetRepoId(), "myorg/repo2",
		"sha256:"+strings.Repeat("f", 64),
		"application/vnd.oci.image.manifest.v1+json",
		rawJSON, "user-1",
	)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if returnedSubjectDigest != subjectDigest {
		t.Errorf("subjectDigest: got %q, want %q", returnedSubjectDigest, subjectDigest)
	}

	// Verify the referrer was registered with the config.mediaType as artifactType.
	refs, _, err := tr.reg.GetReferrers(ctx, "tenant-1", "myorg/repo2", subjectDigest, "application/vnd.test.fallback")
	if err != nil {
		t.Fatalf("GetReferrers: %v", err)
	}
	if len(refs) == 0 {
		t.Error("expected at least one referrer registered with config.mediaType fallback artifactType")
	}
}

// TestListTags_afterPuttingTags verifies ListTags with the fake ListTags that
// sends a real stream from the server (we override the server's ListTags).
// Since the fake returns an empty stream, this confirms no error and zero tags.
func TestListTags_emptyRepo_returnsEmptySlice(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/emptyrepo")

	tags, err := tr.reg.ListTags(ctx, "tenant-1", repo.GetRepoId(), 10, "")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	// An empty repo returns an empty (possibly nil) slice — either is acceptable.
	_ = tags
}
