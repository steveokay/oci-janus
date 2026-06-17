// Package collector_test (sweep_test.go) tests the GC mark-sweep algorithm
// using in-process fake gRPC servers (bufconn + UnimplementedXxxServer).
// No real PostgreSQL, RabbitMQ, or storage backend is required.
package collector

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

const bufSize = 1 << 20 // 1 MiB

// ── fakeMetaServer ────────────────────────────────────────────────────────────

// gcFakeMetaServer is a minimal MetadataServiceServer for GC tests.
// It embeds UnimplementedMetadataServiceServer so only the methods
// the Collector calls need to be implemented.
type gcFakeMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	mu sync.Mutex

	// repos returned by ListRepositories
	repos []*metadatav1.Repository

	// untaggedManifests returned by ListUntaggedManifests, keyed by repoID
	untaggedManifests map[string][]*metadatav1.Manifest

	// orphanedBlobs returned by ListOrphanedBlobs
	orphanedBlobs []*metadatav1.BlobRef

	// deleted manifests and blobs recorded for assertions
	deletedManifestDigests []string
	decrementCalls         []int64
}

func newGCFakeMetaServer() *gcFakeMetaServer {
	return &gcFakeMetaServer{
		untaggedManifests: make(map[string][]*metadatav1.Manifest),
	}
}

// ListRepositories streams all pre-configured repos.
func (s *gcFakeMetaServer) ListRepositories(_ *metadatav1.ListRepositoriesRequest, stream metadatav1.MetadataService_ListRepositoriesServer) error {
	s.mu.Lock()
	repos := append([]*metadatav1.Repository(nil), s.repos...)
	s.mu.Unlock()
	for _, r := range repos {
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return nil
}

// ListUntaggedManifests streams manifests for the given repo that have no tag.
func (s *gcFakeMetaServer) ListUntaggedManifests(req *metadatav1.ListUntaggedManifestsRequest, stream metadatav1.MetadataService_ListUntaggedManifestsServer) error {
	s.mu.Lock()
	manifests := s.untaggedManifests[req.GetRepoId()]
	s.mu.Unlock()
	for _, m := range manifests {
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// DeleteManifest records the deleted digest and returns success.
func (s *gcFakeMetaServer) DeleteManifest(_ context.Context, req *metadatav1.DeleteManifestRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	s.deletedManifestDigests = append(s.deletedManifestDigests, req.GetDigest())
	s.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// ListOrphanedBlobs streams all pre-configured orphaned blobs.
func (s *gcFakeMetaServer) ListOrphanedBlobs(_ *metadatav1.ListOrphanedBlobsRequest, stream metadatav1.MetadataService_ListOrphanedBlobsServer) error {
	s.mu.Lock()
	blobs := append([]*metadatav1.BlobRef(nil), s.orphanedBlobs...)
	s.mu.Unlock()
	for _, b := range blobs {
		if err := stream.Send(b); err != nil {
			return err
		}
	}
	return nil
}

// UnlinkBlob is a no-op in tests.
func (s *gcFakeMetaServer) UnlinkBlob(_ context.Context, _ *metadatav1.UnlinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// DecrementTenantStorage records the decrement call.
func (s *gcFakeMetaServer) DecrementTenantStorage(_ context.Context, req *metadatav1.DecrementTenantStorageRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	s.decrementCalls = append(s.decrementCalls, req.GetBytes())
	s.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// ── fakeStorageServer ─────────────────────────────────────────────────────────

// gcFakeStorageServer records DeleteBlob calls.
type gcFakeStorageServer struct {
	storagev1.UnimplementedStorageServiceServer

	mu          sync.Mutex
	deletedKeys []string
}

func (s *gcFakeStorageServer) DeleteBlob(_ context.Context, req *storagev1.DeleteBlobRequest) (*storagev1.DeleteBlobResponse, error) {
	s.mu.Lock()
	s.deletedKeys = append(s.deletedKeys, req.GetKey())
	s.mu.Unlock()
	return &storagev1.DeleteBlobResponse{}, nil
}

// ── test wiring helpers ───────────────────────────────────────────────────────

// startFakeMetaServer starts an in-process gRPC server serving gcFakeMetaServer
// and returns a client connection to it.
func startFakeMetaServer(t *testing.T, srv *gcFakeMetaServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop(); lis.Close() })

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial fake meta server: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// startFakeStorageServer starts an in-process gRPC server serving gcFakeStorageServer
// and returns a client connection to it.
func startFakeStorageServer(t *testing.T, srv *gcFakeStorageServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop(); lis.Close() })

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial fake storage server: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// noopPublisher discards all RabbitMQ events in GC tests.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ any) error { return nil }

// buildCollector constructs a Collector pointing at the given in-process gRPC servers.
func buildCollector(metaConn, storageConn *grpc.ClientConn, mode string) *Collector {
	return &Collector{
		meta:           metadatav1.NewMetadataServiceClient(metaConn),
		storage:        storagev1.NewStorageServiceClient(storageConn),
		pub:            nil, // nil publisher — tests don't assert on events
		locker:         nil, // no advisory locking in unit tests
		mode:           mode,
		blobMinAge:     0,                // no age gate in unit tests
		manifestMinAge: 0,                // no age gate
	}
}

// manifestOldEnough returns a Manifest with a CreatedAt that is definitely older
// than the GC cutoff (1 year ago). Used to trigger the delete path.
func manifestOldEnough(repoID, tenantID, digest string) *metadatav1.Manifest {
	return &metadatav1.Manifest{
		RepoId:    repoID,
		TenantId:  tenantID,
		Digest:    digest,
		CreatedAt: timestamppb.New(time.Now().Add(-365 * 24 * time.Hour)),
	}
}


// ── tests ─────────────────────────────────────────────────────────────────────

// TestSweepTenantManifests_dryRun verifies that sweep in dry-run mode counts
// eligible manifests but does not call DeleteManifest on the metadata server.
func TestSweepTenantManifests_dryRun(t *testing.T) {
	meta := newGCFakeMetaServer()
	repoID := "repo-111"
	tenantID := "tenant-aaa"
	meta.repos = []*metadatav1.Repository{
		{RepoId: repoID, TenantId: tenantID, Name: "org/repo"},
	}
	meta.untaggedManifests[repoID] = []*metadatav1.Manifest{
		manifestOldEnough(repoID, tenantID, "sha256:aaa"),
		manifestOldEnough(repoID, tenantID, "sha256:bbb"),
	}

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "dry-run")
	n, err := c.sweepTenantManifests(context.Background(), tenantID, true)
	if err != nil {
		t.Fatalf("sweepTenantManifests dry-run: %v", err)
	}
	if n != 2 {
		t.Errorf("dry-run: expected 2 manifests counted, got %d", n)
	}

	meta.mu.Lock()
	deletedCount := len(meta.deletedManifestDigests)
	meta.mu.Unlock()

	if deletedCount != 0 {
		t.Errorf("dry-run: expected 0 DeleteManifest calls, got %d", deletedCount)
	}
}

// TestSweepTenantManifests_fullMode verifies that manifests are actually deleted
// when dryRun=false.
func TestSweepTenantManifests_fullMode(t *testing.T) {
	meta := newGCFakeMetaServer()
	repoID := "repo-222"
	tenantID := "tenant-bbb"
	meta.repos = []*metadatav1.Repository{
		{RepoId: repoID, TenantId: tenantID, Name: "org/repo2"},
	}
	meta.untaggedManifests[repoID] = []*metadatav1.Manifest{
		manifestOldEnough(repoID, tenantID, "sha256:ccc"),
	}

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "manifests")
	n, err := c.sweepTenantManifests(context.Background(), tenantID, false)
	if err != nil {
		t.Fatalf("sweepTenantManifests full: %v", err)
	}
	if n != 1 {
		t.Errorf("full mode: expected 1 manifest deleted, got %d", n)
	}

	meta.mu.Lock()
	digests := append([]string(nil), meta.deletedManifestDigests...)
	meta.mu.Unlock()

	if len(digests) != 1 || digests[0] != "sha256:ccc" {
		t.Errorf("DeleteManifest called with wrong digest(s): %v", digests)
	}
}

// TestSweepTenantManifests_noUntagged verifies that the sweep returns 0 when
// there are no untagged manifests.
func TestSweepTenantManifests_noUntagged(t *testing.T) {
	meta := newGCFakeMetaServer()
	repoID := "repo-333"
	tenantID := "tenant-ccc"
	meta.repos = []*metadatav1.Repository{
		{RepoId: repoID, TenantId: tenantID, Name: "org/repo3"},
	}
	// no untagged manifests configured

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "manifests")
	n, err := c.sweepTenantManifests(context.Background(), tenantID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions, got %d", n)
	}
}

// TestSweepTenantManifests_manifestTooYoung verifies that when manifestMinAge
// is non-zero, manifests created recently are not deleted.
// Here we set manifestMinAge = 24h and provide a manifest created 1 hour ago.
func TestSweepTenantManifests_manifestTooYoung(t *testing.T) {
	meta := newGCFakeMetaServer()
	repoID := "repo-444"
	tenantID := "tenant-ddd"
	meta.repos = []*metadatav1.Repository{
		{RepoId: repoID, TenantId: tenantID, Name: "org/repo4"},
	}
	// Manifest created 1 hour ago — within the 24h min age.
	meta.untaggedManifests[repoID] = []*metadatav1.Manifest{
		{
			RepoId:    repoID,
			TenantId:  tenantID,
			Digest:    "sha256:young",
			CreatedAt: timestamppb.New(time.Now().Add(-time.Hour)),
		},
	}

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	// Set manifestMinAge = 24h so the 1-hour-old manifest is skipped.
	c := buildCollector(metaConn, storageConn, "manifests")
	c.manifestMinAge = 24 * time.Hour

	n, err := c.sweepTenantManifests(context.Background(), tenantID, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("too-young manifest should be skipped; expected 0 deletions, got %d", n)
	}
}

// TestSweepTenantBlobs_dryRun verifies that orphaned blobs are counted but not
// deleted in dry-run mode.
func TestSweepTenantBlobs_dryRun(t *testing.T) {
	meta := newGCFakeMetaServer()
	tenantID := "tenant-eee"
	meta.orphanedBlobs = []*metadatav1.BlobRef{
		{Digest: "sha256:orphan1", StorageKey: "blobs/tenant-eee/sha256/or/orphan1", SizeBytes: 512},
		{Digest: "sha256:orphan2", StorageKey: "blobs/tenant-eee/sha256/or/orphan2", SizeBytes: 1024},
	}

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "dry-run")
	n, freed, err := c.sweepTenantBlobs(context.Background(), tenantID, true)
	if err != nil {
		t.Fatalf("sweepTenantBlobs dry-run: %v", err)
	}
	if n != 2 {
		t.Errorf("dry-run: expected 2 blobs counted, got %d", n)
	}
	if freed != 1536 {
		t.Errorf("dry-run: expected 1536 bytes counted, got %d", freed)
	}

	storage.mu.Lock()
	storageDeletes := len(storage.deletedKeys)
	storage.mu.Unlock()

	if storageDeletes != 0 {
		t.Errorf("dry-run: expected 0 storage deletes, got %d", storageDeletes)
	}
}

// TestSweepTenantBlobs_fullMode verifies that orphaned blobs are deleted from
// storage and the quota is decremented.
func TestSweepTenantBlobs_fullMode(t *testing.T) {
	meta := newGCFakeMetaServer()
	tenantID := "tenant-fff"
	storageKey := "blobs/tenant-fff/sha256/ab/abc123"
	meta.orphanedBlobs = []*metadatav1.BlobRef{
		{Digest: "sha256:abc123", StorageKey: storageKey, SizeBytes: 2048},
	}

	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "blobs")
	n, freed, err := c.sweepTenantBlobs(context.Background(), tenantID, false)
	if err != nil {
		t.Fatalf("sweepTenantBlobs full: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 blob deleted, got %d", n)
	}
	if freed != 2048 {
		t.Errorf("expected 2048 bytes freed, got %d", freed)
	}

	storage.mu.Lock()
	keys := append([]string(nil), storage.deletedKeys...)
	storage.mu.Unlock()

	if len(keys) != 1 || keys[0] != storageKey {
		t.Errorf("storage DeleteBlob called with wrong key(s): %v", keys)
	}

	meta.mu.Lock()
	decrements := append([]int64(nil), meta.decrementCalls...)
	meta.mu.Unlock()

	if len(decrements) != 1 || decrements[0] != 2048 {
		t.Errorf("DecrementTenantStorage not called correctly: %v", decrements)
	}
}

// TestSweepTenantBlobs_noOrphans verifies that the sweep returns 0 when there
// are no orphaned blobs.
func TestSweepTenantBlobs_noOrphans(t *testing.T) {
	meta := newGCFakeMetaServer()
	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "blobs")
	n, freed, err := c.sweepTenantBlobs(context.Background(), "tenant-zzz", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || freed != 0 {
		t.Errorf("expected 0 blobs / 0 bytes, got %d / %d", n, freed)
	}
}

// TestListTenants_extractsUniqueTenants verifies that listTenants() returns the
// distinct tenant IDs from the repository stream.
func TestListTenants_extractsUniqueTenants(t *testing.T) {
	meta := newGCFakeMetaServer()
	meta.repos = []*metadatav1.Repository{
		{RepoId: "r1", TenantId: "tenant-111", Name: "org/a"},
		{RepoId: "r2", TenantId: "tenant-111", Name: "org/b"}, // same tenant
		{RepoId: "r3", TenantId: "tenant-222", Name: "org/c"},
	}
	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "full")
	tenants, err := c.listTenants(context.Background())
	if err != nil {
		t.Fatalf("listTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Errorf("expected 2 unique tenant IDs, got %d: %v", len(tenants), tenants)
	}
	if _, ok := tenants["tenant-111"]; !ok {
		t.Error("expected tenant-111 in tenant set")
	}
	if _, ok := tenants["tenant-222"]; !ok {
		t.Error("expected tenant-222 in tenant set")
	}
}

// TestListTenants_empty verifies that listTenants returns an empty map when
// there are no repositories.
func TestListTenants_empty(t *testing.T) {
	meta := newGCFakeMetaServer() // no repos
	storage := &gcFakeStorageServer{}
	metaConn := startFakeMetaServer(t, meta)
	storageConn := startFakeStorageServer(t, storage)

	c := buildCollector(metaConn, storageConn, "full")
	tenants, err := c.listTenants(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tenants) != 0 {
		t.Errorf("expected empty tenant map, got %d entries", len(tenants))
	}
}

// Ensure io.EOF is imported (used for clarity of streaming protocol).
var _ = io.EOF
