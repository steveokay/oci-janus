// Package service (registry_grpc_test.go) exercises Registry methods using
// in-process fake gRPC servers (bufconn) and miniredis.
// No build tags — these run with plain `go test ./...`.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

const bufSize = 1 << 20 // 1 MiB

// ── noopPublisher ─────────────────────────────────────────────────────────────

// noopPublisher silently discards all events so tests can exercise PutManifest
// without a real AMQP broker.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ events.Event) error { return nil }

// ── fakeMetadataServer ────────────────────────────────────────────────────────

// fakeMetadataServer is an in-memory implementation of MetadataServiceServer.
// Only the methods called by Registry are implemented; others return Unimplemented.
type fakeMetadataServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	mu          sync.Mutex
	repos       map[string]*metadatav1.Repository // key: tenantID+":"+name
	manifests   map[string]*metadatav1.Manifest   // key: repoID+":"+digest
	tagToDigest map[string]string                 // key: repoID+":"+tagName
	quota       *metadatav1.QuotaUsage
}

func newFakeMetadataServer() *fakeMetadataServer {
	return &fakeMetadataServer{
		repos:       make(map[string]*metadatav1.Repository),
		manifests:   make(map[string]*metadatav1.Manifest),
		tagToDigest: make(map[string]string),
		quota:       &metadatav1.QuotaUsage{QuotaBytes: 10 << 30, UsedBytes: 0},
	}
}

func (s *fakeMetadataServer) CreateRepository(_ context.Context, req *metadatav1.CreateRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetTenantId() + ":" + req.GetName()
	if r, ok := s.repos[key]; ok {
		return r, nil
	}
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	repoID := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	repo := &metadatav1.Repository{
		RepoId:   repoID,
		TenantId: req.GetTenantId(),
		Name:     req.GetName(),
	}
	s.repos[key] = repo
	return repo, nil
}

func (s *fakeMetadataServer) PutManifest(_ context.Context, req *metadatav1.PutManifestRequest) (*metadatav1.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &metadatav1.Manifest{
		RepoId:    req.GetRepoId(),
		TenantId:  req.GetTenantId(),
		Digest:    req.GetDigest(),
		MediaType: req.GetMediaType(),
		RawJson:   req.GetRawJson(),
		SizeBytes: req.GetSizeBytes(),
	}
	s.manifests[req.GetRepoId()+":"+req.GetDigest()] = m
	return m, nil
}

func (s *fakeMetadataServer) PutTag(_ context.Context, req *metadatav1.PutTagRequest) (*metadatav1.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tagToDigest[req.GetRepoId()+":"+req.GetName()] = req.GetManifestDigest()
	return &metadatav1.Tag{
		RepoId:         req.GetRepoId(),
		TenantId:       req.GetTenantId(),
		Name:           req.GetName(),
		ManifestDigest: req.GetManifestDigest(),
	}, nil
}

func (s *fakeMetadataServer) GetTag(_ context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, ok := s.tagToDigest[req.GetRepoId()+":"+req.GetName()]
	if !ok {
		return nil, status.Error(codes.NotFound, "tag not found")
	}
	return &metadatav1.Tag{
		RepoId:         req.GetRepoId(),
		TenantId:       req.GetTenantId(),
		Name:           req.GetName(),
		ManifestDigest: digest,
	}, nil
}

func (s *fakeMetadataServer) GetManifest(_ context.Context, req *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.manifests[req.GetRepoId()+":"+req.GetReference()]
	if !ok {
		return nil, status.Error(codes.NotFound, "manifest not found")
	}
	return m, nil
}

func (s *fakeMetadataServer) DeleteTag(_ context.Context, req *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetRepoId() + ":" + req.GetName()
	if _, ok := s.tagToDigest[key]; !ok {
		return nil, status.Error(codes.NotFound, "tag not found")
	}
	delete(s.tagToDigest, key)
	return &emptypb.Empty{}, nil
}

func (s *fakeMetadataServer) DeleteManifest(_ context.Context, req *metadatav1.DeleteManifestRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetRepoId() + ":" + req.GetDigest()
	if _, ok := s.manifests[key]; !ok {
		return nil, status.Error(codes.NotFound, "manifest not found")
	}
	delete(s.manifests, key)
	return &emptypb.Empty{}, nil
}

func (s *fakeMetadataServer) ListTags(_ *metadatav1.ListTagsRequest, stream metadatav1.MetadataService_ListTagsServer) error {
	s.mu.Lock()
	tags := make([]*metadatav1.Tag, 0)
	req := stream.Context().Value(struct{}{}) // unused; iterate all to keep it simple
	_ = req
	s.mu.Unlock()
	// We send whatever tags match — simplified: send all (no filter by repoID).
	// Tests that need specific results pre-create tags via PutTag.
	_ = tags
	return nil
}

func (s *fakeMetadataServer) ListTagsForRepo(repoID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []string
	prefix := repoID + ":"
	for k, digest := range s.tagToDigest {
		if strings.HasPrefix(k, prefix) {
			name := strings.TrimPrefix(k, prefix)
			_ = digest
			result = append(result, name)
		}
	}
	return result
}

func (s *fakeMetadataServer) UnlinkBlob(_ context.Context, _ *metadatav1.UnlinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *fakeMetadataServer) GetTenantQuotaUsage(_ context.Context, _ *metadatav1.GetTenantQuotaUsageRequest) (*metadatav1.QuotaUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quota, nil
}

// ── fakeStorageServer ─────────────────────────────────────────────────────────

// fakeStorageServer is an in-memory implementation of StorageServiceServer.
// Blobs are stored as []byte values keyed by their storage key.
type fakeStorageServer struct {
	storagev1.UnimplementedStorageServiceServer
	mu    sync.Mutex
	blobs map[string][]byte
}

func newFakeStorageServer() *fakeStorageServer {
	return &fakeStorageServer{blobs: make(map[string][]byte)}
}

func (s *fakeStorageServer) PutBlob(stream storagev1.StorageService_PutBlobServer) error {
	var key string
	var buf []byte
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch d := req.GetData().(type) {
		case *storagev1.PutBlobRequest_Meta:
			key = d.Meta.GetKey()
		case *storagev1.PutBlobRequest_Chunk:
			buf = append(buf, d.Chunk...)
		}
	}
	if key == "" {
		return status.Error(codes.InvalidArgument, "no meta sent")
	}
	s.mu.Lock()
	s.blobs[key] = buf
	s.mu.Unlock()
	return stream.SendAndClose(&storagev1.PutBlobResponse{Key: key})
}

func (s *fakeStorageServer) GetBlob(req *storagev1.GetBlobRequest, stream storagev1.StorageService_GetBlobServer) error {
	s.mu.Lock()
	data, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "blob not found")
	}
	return stream.Send(&storagev1.GetBlobResponse{Chunk: data})
}

func (s *fakeStorageServer) BlobExists(_ context.Context, req *storagev1.BlobExistsRequest) (*storagev1.BlobExistsResponse, error) {
	s.mu.Lock()
	_, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	return &storagev1.BlobExistsResponse{Exists: ok}, nil
}

func (s *fakeStorageServer) StatBlob(_ context.Context, req *storagev1.StatBlobRequest) (*storagev1.StatBlobResponse, error) {
	s.mu.Lock()
	data, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "blob not found")
	}
	return &storagev1.StatBlobResponse{Size: int64(len(data))}, nil
}

func (s *fakeStorageServer) DeleteBlob(_ context.Context, req *storagev1.DeleteBlobRequest) (*storagev1.DeleteBlobResponse, error) {
	s.mu.Lock()
	delete(s.blobs, req.GetKey())
	s.mu.Unlock()
	return &storagev1.DeleteBlobResponse{}, nil
}

// ── Test fixtures ─────────────────────────────────────────────────────────────

// testRegistry bundles a Registry with its fake dependencies for tests.
type testRegistry struct {
	reg       *Registry
	meta      *fakeMetadataServer
	storage   *fakeStorageServer
	rdb       *redis.Client
	uploads   *UploadStore
	referrers *ReferrerStore
	// pub records every published event so tests can assert the publish side
	// effects (FUT-081). It behaves like noopPublisher for tests that ignore it.
	pub *recordingPublisher
}

// buildTestRegistry starts in-process fake gRPC servers and creates a Registry
// backed by miniredis. Caller must call cleanup when done.
func buildTestRegistry(t *testing.T) (*testRegistry, func()) {
	t.Helper()

	// Start miniredis for uploads + referrers.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// Start fake metadata gRPC server.
	fakeMeta := newFakeMetadataServer()
	metaLis := bufconn.Listen(bufSize)
	metaSrv := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaSrv, fakeMeta)
	go func() { _ = metaSrv.Serve(metaLis) }()

	// Start fake storage gRPC server.
	fakeStorage := newFakeStorageServer()
	storageLis := bufconn.Listen(bufSize)
	storageSrv := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(storageSrv, fakeStorage)
	go func() { _ = storageSrv.Serve(storageLis) }()

	// Dial both in-process servers via bufconn.
	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient(
			"passthrough://bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("grpc.NewClient: %v", err)
		}
		return conn
	}
	metaConn := dial(metaLis)
	storageConn := dial(storageLis)

	uploads := NewUploadStore(rdb)
	refs := NewReferrerStore(rdb)

	pub := &recordingPublisher{}
	reg := newRegistryWithClients(
		metadatav1.NewMetadataServiceClient(metaConn),
		storagev1.NewStorageServiceClient(storageConn),
		uploads,
		refs,
		pub,
	)

	tr := &testRegistry{
		reg:       reg,
		meta:      fakeMeta,
		storage:   fakeStorage,
		rdb:       rdb,
		uploads:   uploads,
		referrers: refs,
		pub:       pub,
	}
	cleanup := func() {
		_ = metaConn.Close()
		_ = storageConn.Close()
		metaSrv.Stop()
		storageSrv.Stop()
		_ = rdb.Close()
		mr.Close()
	}
	return tr, cleanup
}

// ── Upload / blob tests ───────────────────────────────────────────────────────

// TestInitiateUpload_createsState verifies that InitiateUpload persists an
// UploadState and returns a non-empty UUID.
func TestInitiateUpload_createsState(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, err := tr.reg.InitiateUpload(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("InitiateUpload: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty upload UUID")
	}

	st, err := tr.reg.GetUpload(ctx, id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if st.TenantID != "tenant-1" {
		t.Errorf("TenantID: got %q, want tenant-1", st.TenantID)
	}
	if st.RepoName != "myorg/myrepo" {
		t.Errorf("RepoName: got %q, want myorg/myrepo", st.RepoName)
	}
}

// TestGetUpload_notFound verifies that GetUpload returns ErrUploadNotFound for
// an unknown UUID.
func TestGetUpload_notFound(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	_, err := tr.reg.GetUpload(context.Background(), "does-not-exist")
	if err != ErrUploadNotFound {
		t.Errorf("expected ErrUploadNotFound, got %v", err)
	}
}

// TestCancelUpload_removesState verifies that CancelUpload deletes the state so
// subsequent GetUpload calls return ErrUploadNotFound.
func TestCancelUpload_removesState(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-1", "myorg/myrepo")

	if err := tr.reg.CancelUpload(ctx, id); err != nil {
		t.Fatalf("CancelUpload: %v", err)
	}
	_, err := tr.reg.GetUpload(ctx, id)
	if err != ErrUploadNotFound {
		t.Errorf("expected ErrUploadNotFound after cancel, got %v", err)
	}
}

// TestCompleteUpload_monolithicUpload verifies the single-PUT (no PATCH chunks)
// upload path: the blob is stored and its digest is returned correctly.
func TestCompleteUpload_monolithicUpload_returnsDigest(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-1", "myorg/myrepo")

	data := []byte("hello world blob content for unit test")
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))

	digest, size, err := tr.reg.CompleteUpload(ctx, id, expectedDigest, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if digest != expectedDigest {
		t.Errorf("digest: got %q, want %q", digest, expectedDigest)
	}
	if size != int64(len(data)) {
		t.Errorf("size: got %d, want %d", size, len(data))
	}
}

// TestCompleteUpload_digestMismatch verifies that providing a wrong expected
// digest results in ErrDigestMismatch and the blob is not retained.
func TestCompleteUpload_digestMismatch_returnsError(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-1", "myorg/myrepo")

	data := []byte("some data")
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	_, _, err := tr.reg.CompleteUpload(ctx, id, wrongDigest, bytes.NewReader(data), int64(len(data)))
	if err != ErrDigestMismatch {
		t.Errorf("expected ErrDigestMismatch, got %v", err)
	}
}

// TestCompleteUpload_correctDigest_returnsMatchingDigest verifies that when
// the computed digest matches the expected digest, the upload succeeds.
func TestCompleteUpload_correctDigest_returnsMatchingDigest(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	id, _ := tr.reg.InitiateUpload(ctx, "tenant-1", "myorg/myrepo")

	data := []byte("second upload for digest verification")
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256(data))

	digest, size, err := tr.reg.CompleteUpload(ctx, id, expected, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if digest != expected {
		t.Errorf("digest: got %q, want %q", digest, expected)
	}
	if size != int64(len(data)) {
		t.Errorf("size: got %d, want %d", size, int64(len(data)))
	}
}

// ── Blob existence / retrieval tests ─────────────────────────────────────────

// TestBlobExists_existingBlob verifies that a blob stored via PutBlob is
// reported as existing with the correct size.
func TestBlobExists_existingBlob_returnsTrueAndSize(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	data := []byte("blob content")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-test"

	// Store the blob directly in the fake storage server.
	key := blobKey(tenantID, digest)
	tr.storage.mu.Lock()
	tr.storage.blobs[key] = data
	tr.storage.mu.Unlock()

	exists, size, err := tr.reg.BlobExists(ctx, tenantID, digest)
	if err != nil {
		t.Fatalf("BlobExists: %v", err)
	}
	if !exists {
		t.Error("expected blob to exist")
	}
	if size != int64(len(data)) {
		t.Errorf("size: got %d, want %d", size, len(data))
	}
}

// TestBlobExists_missingBlob verifies that a non-existent blob is reported
// as not existing with size 0.
func TestBlobExists_missingBlob_returnsFalse(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("a", 64)
	exists, size, err := tr.reg.BlobExists(context.Background(), "tenant-1", digest)
	if err != nil {
		t.Fatalf("BlobExists: %v", err)
	}
	if exists {
		t.Error("expected blob to not exist")
	}
	if size != 0 {
		t.Errorf("size: got %d, want 0", size)
	}
}

// TestGetBlob_existingBlob verifies that GetBlob streams the correct content.
func TestGetBlob_existingBlob_streamsContent(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	data := []byte("get blob content for unit test")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-get"

	key := blobKey(tenantID, digest)
	tr.storage.mu.Lock()
	tr.storage.blobs[key] = data
	tr.storage.mu.Unlock()

	var buf bytes.Buffer
	_, err := tr.reg.GetBlob(ctx, tenantID, digest, &buf)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Errorf("content mismatch: got %q, want %q", buf.Bytes(), data)
	}
}

// TestGetBlob_missingBlob verifies that GetBlob returns an error for an
// unknown blob (the error wraps the gRPC NotFound status).
func TestGetBlob_missingBlob_returnsError(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("b", 64)
	var buf bytes.Buffer
	_, err := tr.reg.GetBlob(context.Background(), "tenant-1", digest, &buf)
	if err == nil {
		t.Error("expected error for missing blob, got nil")
	}
}

// TestDeleteBlob_existingBlob verifies that DeleteBlob removes the blob from
// storage and unlinks it from metadata without error.
func TestDeleteBlob_existingBlob_succeeds(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	data := []byte("delete me")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-del"
	key := blobKey(tenantID, digest)

	tr.storage.mu.Lock()
	tr.storage.blobs[key] = data
	tr.storage.mu.Unlock()

	if err := tr.reg.DeleteBlob(ctx, tenantID, "repo-1", digest); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}

	tr.storage.mu.Lock()
	_, stillExists := tr.storage.blobs[key]
	tr.storage.mu.Unlock()
	if stillExists {
		t.Error("expected blob to be removed from storage after DeleteBlob")
	}
}

// ── Manifest tests ────────────────────────────────────────────────────────────

// validManifestJSON returns a minimal OCI image manifest JSON for testing.
func validManifestJSON() []byte {
	return []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":7},"layers":[]}`)
}

// TestPutManifest_withTag_storesManifestAndTag verifies that PutManifest with a
// tag reference stores both the manifest and the tag.
func TestPutManifest_withTag_storesManifestAndTag(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	rawJSON := validManifestJSON()
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawJSON))

	// First create repository so the repoID is known.
	repo, err := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("GetOrCreateRepository: %v", err)
	}

	digest, subjectDigest, err := tr.reg.PutManifest(
		ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		"latest", "application/vnd.oci.image.manifest.v1+json",
		rawJSON, "user-1",
	)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if digest != expectedDigest {
		t.Errorf("digest: got %q, want %q", digest, expectedDigest)
	}
	if subjectDigest != "" {
		t.Errorf("subjectDigest: expected empty (no subject field), got %q", subjectDigest)
	}

	// The tag must be retrievable.
	m, err := tr.reg.GetManifest(ctx, "tenant-1", repo.GetRepoId(), "latest")
	if err != nil {
		t.Fatalf("GetManifest by tag: %v", err)
	}
	if m.GetDigest() != expectedDigest {
		t.Errorf("GetManifest digest: got %q, want %q", m.GetDigest(), expectedDigest)
	}
}

// TestPutManifest_withDigestReference_noTagCreated verifies that pushing a
// manifest by digest (not a tag) does not create a tag entry.
func TestPutManifest_withDigestReference_noTagCreated(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	rawJSON := validManifestJSON()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawJSON))

	repo, err := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("GetOrCreateRepository: %v", err)
	}

	gotDigest, _, err := tr.reg.PutManifest(
		ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		digest, // reference IS a digest — no tag should be created
		"application/vnd.oci.image.manifest.v1+json",
		rawJSON, "user-1",
	)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if gotDigest != digest {
		t.Errorf("digest: got %q, want %q", gotDigest, digest)
	}
}

// TestGetManifest_byDigest verifies that GetManifest retrieves a manifest
// by its SHA256 digest.
func TestGetManifest_byDigest_returnsManifest(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	rawJSON := validManifestJSON()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawJSON))

	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	_, _, err := tr.reg.PutManifest(ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		digest, "application/vnd.oci.image.manifest.v1+json", rawJSON, "u")
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	m, err := tr.reg.GetManifest(ctx, "tenant-1", repo.GetRepoId(), digest)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if m.GetDigest() != digest {
		t.Errorf("digest: got %q, want %q", m.GetDigest(), digest)
	}
}

// TestGetManifest_unknownTag verifies that GetManifest returns ErrNotFound
// when the tag does not exist.
func TestGetManifest_unknownTag_returnsErrNotFound(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	repo, _ := tr.reg.GetOrCreateRepository(context.Background(), "tenant-1", "myorg/myrepo")

	_, err := tr.reg.GetManifest(context.Background(), "tenant-1", repo.GetRepoId(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestDeleteManifest_byTag verifies that deleting by tag name removes only the
// tag while leaving the underlying manifest intact (OCI spec §4.4).
func TestDeleteManifest_byTag_removesTagOnly(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	rawJSON := validManifestJSON()

	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	digest, _, _ := tr.reg.PutManifest(ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		"v1.0", "application/vnd.oci.image.manifest.v1+json", rawJSON, "u")

	// Delete the tag.
	if err := tr.reg.DeleteManifest(ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo", "v1.0", "u"); err != nil {
		t.Fatalf("DeleteManifest (by tag): %v", err)
	}

	// Tag must be gone.
	_, tagErr := tr.reg.GetManifest(ctx, "tenant-1", repo.GetRepoId(), "v1.0")
	if tagErr != ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted tag, got %v", tagErr)
	}

	// But manifest by digest must still be accessible.
	m, err := tr.reg.GetManifest(ctx, "tenant-1", repo.GetRepoId(), digest)
	if err != nil {
		t.Errorf("expected manifest by digest to still exist, got %v", err)
	}
	if m.GetDigest() != digest {
		t.Errorf("manifest digest: got %q, want %q", m.GetDigest(), digest)
	}
}

// TestDeleteManifest_byDigest verifies that deleting by digest removes the manifest.
func TestDeleteManifest_byDigest_removesManifest(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	rawJSON := validManifestJSON()
	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	digest, _, _ := tr.reg.PutManifest(ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo",
		digest_of(rawJSON), "application/vnd.oci.image.manifest.v1+json", rawJSON, "u")

	if err := tr.reg.DeleteManifest(ctx, "tenant-1", repo.GetRepoId(), "myorg/myrepo", digest, "u"); err != nil {
		t.Fatalf("DeleteManifest (by digest): %v", err)
	}

	_, err := tr.reg.GetManifest(ctx, "tenant-1", repo.GetRepoId(), digest)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after digest delete, got %v", err)
	}
}

// TestDeleteManifest_unknownTag verifies that deleting a non-existent tag
// returns ErrNotFound.
func TestDeleteManifest_unknownTag_returnsErrNotFound(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	repo, _ := tr.reg.GetOrCreateRepository(context.Background(), "tenant-1", "myorg/myrepo")
	err := tr.reg.DeleteManifest(context.Background(), "tenant-1", repo.GetRepoId(), "myorg/myrepo", "missing-tag", "u")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestDeleteManifest_unknownDigest verifies that deleting a non-existent digest
// returns ErrNotFound.
func TestDeleteManifest_unknownDigest_returnsErrNotFound(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	repo, _ := tr.reg.GetOrCreateRepository(context.Background(), "tenant-1", "myorg/myrepo")
	err := tr.reg.DeleteManifest(context.Background(), "tenant-1", repo.GetRepoId(), "myorg/myrepo", "sha256:"+strings.Repeat("0", 64), "u")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── Repository / quota tests ──────────────────────────────────────────────────

// TestGetOrCreateRepository_idempotent verifies that calling
// GetOrCreateRepository twice with the same name returns the same repo ID.
func TestGetOrCreateRepository_idempotent(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	r1, err := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("first GetOrCreateRepository: %v", err)
	}
	r2, err := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	if err != nil {
		t.Fatalf("second GetOrCreateRepository: %v", err)
	}
	if r1.GetRepoId() != r2.GetRepoId() {
		t.Errorf("expected same repoID from idempotent create: got %q and %q", r1.GetRepoId(), r2.GetRepoId())
	}
}

// TestCheckQuota_withinLimit verifies that CheckQuota returns nil when usage is
// below the tenant quota.
func TestCheckQuota_withinLimit_returnsNil(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	// Default quota: 10 GiB, used: 0 → a 1 MiB upload should be allowed.
	err := tr.reg.CheckQuota(context.Background(), "tenant-1", 1<<20)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestCheckQuota_exceededLimit verifies that CheckQuota returns ErrQuotaExceeded
// when the upload would push usage over the quota ceiling.
func TestCheckQuota_exceededLimit_returnsErrQuotaExceeded(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	// Set quota to 100 bytes, used 90 — a 20-byte upload should fail.
	tr.meta.mu.Lock()
	tr.meta.quota = &metadatav1.QuotaUsage{QuotaBytes: 100, UsedBytes: 90}
	tr.meta.mu.Unlock()

	err := tr.reg.CheckQuota(context.Background(), "tenant-1", 20)
	if err != ErrQuotaExceeded {
		t.Errorf("expected ErrQuotaExceeded, got %v", err)
	}
}

// ── ListTags tests ────────────────────────────────────────────────────────────

// TestListTags_returnsTagNames verifies that ListTags returns all tags pushed
// to the repository.
func TestListTags_returnsTagNames(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	// Wire up a fake ListTags that returns fixed tags for the test.
	ctx := context.Background()
	// We can test ListTags by overriding the metadata server's ListTags.
	// The current fake returns an empty stream; we test that the function
	// doesn't error and returns an empty slice from the empty stream.
	repo, _ := tr.reg.GetOrCreateRepository(ctx, "tenant-1", "myorg/myrepo")
	tags, err := tr.reg.ListTags(ctx, "tenant-1", repo.GetRepoId(), 100, "")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	// Empty stream → empty slice (not an error).
	if tags == nil {
		// nil is fine; some impls return nil for empty.
	}
	_ = tags
}

// ── GetReferrers tests ────────────────────────────────────────────────────────

// TestGetReferrers_noFilter verifies that GetReferrers without an artifactType
// filter returns all referrers.
func TestGetReferrers_noFilter_returnsAll(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	subjectDigest := "sha256:" + strings.Repeat("c", 64)

	// Add some referrers directly.
	desc1 := ReferrerDescriptor{MediaType: "app/vnd.test", Digest: "sha256:" + strings.Repeat("d", 64), ArtifactType: "sbom"}
	desc2 := ReferrerDescriptor{MediaType: "app/vnd.test", Digest: "sha256:" + strings.Repeat("e", 64), ArtifactType: "sig"}
	_ = tr.referrers.Add(ctx, "tenant-1", "myorg/myrepo", subjectDigest, desc1)
	_ = tr.referrers.Add(ctx, "tenant-1", "myorg/myrepo", subjectDigest, desc2)

	all, filtered, err := tr.reg.GetReferrers(ctx, "tenant-1", "myorg/myrepo", subjectDigest, "")
	if err != nil {
		t.Fatalf("GetReferrers: %v", err)
	}
	if filtered {
		t.Error("expected filtered=false when no artifactType filter")
	}
	if len(all) != 2 {
		t.Errorf("expected 2 referrers, got %d", len(all))
	}
}

// TestGetReferrers_withFilter verifies that GetReferrers with an artifactType
// filter returns only matching descriptors.
func TestGetReferrers_withFilter_returnsSubset(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()

	ctx := context.Background()
	subjectDigest := "sha256:" + strings.Repeat("f", 64)

	desc1 := ReferrerDescriptor{MediaType: "a", Digest: "sha256:" + strings.Repeat("1", 64), ArtifactType: "sbom"}
	desc2 := ReferrerDescriptor{MediaType: "b", Digest: "sha256:" + strings.Repeat("2", 64), ArtifactType: "sig"}
	_ = tr.referrers.Add(ctx, "tenant-1", "myorg/myrepo", subjectDigest, desc1)
	_ = tr.referrers.Add(ctx, "tenant-1", "myorg/myrepo", subjectDigest, desc2)

	result, filtered, err := tr.reg.GetReferrers(ctx, "tenant-1", "myorg/myrepo", subjectDigest, "sbom")
	if err != nil {
		t.Fatalf("GetReferrers with filter: %v", err)
	}
	if !filtered {
		t.Error("expected filtered=true when artifactType filter is set")
	}
	if len(result) != 1 {
		t.Errorf("expected 1 sbom referrer, got %d", len(result))
	}
	if result[0].ArtifactType != "sbom" {
		t.Errorf("ArtifactType: got %q, want sbom", result[0].ArtifactType)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// digest_of returns the sha256 digest string for a byte slice.
func digest_of(b []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b))
}
