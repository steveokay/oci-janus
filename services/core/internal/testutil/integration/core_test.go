//go:build integration

// Package integration contains end-to-end OCI HTTP tests for registry-core.
// It uses in-process gRPC servers (via bufconn) for the auth, metadata, and storage
// dependencies, and real Redis + RabbitMQ containers for the stateful components.
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/core/internal/handler"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

const (
	bufSize  = 1024 * 1024 // 1 MiB listener buffer for in-process gRPC
	tenantID = "98dbe36b-ef28-4903-b25c-bff1b2921c9e"
)

// ── In-process gRPC mock servers ─────────────────────────────────────────────

// mockAuthServer validates any non-empty token and returns a fixed tenant/user.
type mockAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *mockAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	if req.GetToken() == "" {
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
	// Accept any non-empty token and return a fixed user with push+pull on all repos.
	return &authv1.ValidateTokenResponse{
		Valid:    true,
		UserId:   "test-user",
		TenantId: tenantID,
		Access: []*authv1.RepositoryAccess{
			{Type: "repository", Name: "*", Actions: []string{"push", "pull", "delete"}},
		},
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
	}, nil
}

// mockMetadataServer stores repositories, manifests, and tags in memory.
type mockMetadataServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	mu         sync.Mutex
	repos      map[string]*metadatav1.Repository // key: tenantID+":"+repoName
	manifests  map[string]*metadatav1.Manifest   // key: repoID+":"+digest
	tagToDigest map[string]string                // key: repoID+":"+tagName
}

func newMockMetadataServer() *mockMetadataServer {
	return &mockMetadataServer{
		repos:       make(map[string]*metadatav1.Repository),
		manifests:   make(map[string]*metadatav1.Manifest),
		tagToDigest: make(map[string]string),
	}
}

func (s *mockMetadataServer) CreateRepository(_ context.Context, req *metadatav1.CreateRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := req.GetTenantId() + ":" + req.GetName()
	if r, ok := s.repos[key]; ok {
		// return existing repo (idempotent create)
		return r, nil
	}
	// Generate a deterministic fake UUID from the repo name to keep it stable.
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	repoID := digest[:8] + "-" + digest[8:12] + "-" + digest[12:16] + "-" + digest[16:20] + "-" + digest[20:32]
	repo := &metadatav1.Repository{
		RepoId:       repoID,
		Name:         req.GetName(),
		TenantId:     req.GetTenantId(),
		StorageQuota: 10 << 30, // 10 GiB
	}
	s.repos[key] = repo
	return repo, nil
}

func (s *mockMetadataServer) GetRepository(_ context.Context, req *metadatav1.GetRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.repos {
		if r.GetRepoId() == req.GetRepoId() {
			return r, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "repository not found")
}

func (s *mockMetadataServer) PutManifest(_ context.Context, req *metadatav1.PutManifestRequest) (*metadatav1.Manifest, error) {
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

func (s *mockMetadataServer) GetManifest(_ context.Context, req *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.manifests[req.GetRepoId()+":"+req.GetDigest()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "manifest not found")
	}
	return m, nil
}

func (s *mockMetadataServer) PutTag(_ context.Context, req *metadatav1.PutTagRequest) (*metadatav1.Tag, error) {
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

func (s *mockMetadataServer) GetTag(_ context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	digest, ok := s.tagToDigest[req.GetRepoId()+":"+req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "tag not found")
	}
	return &metadatav1.Tag{
		RepoId:         req.GetRepoId(),
		TenantId:       req.GetTenantId(),
		Name:           req.GetName(),
		ManifestDigest: digest,
	}, nil
}

func (s *mockMetadataServer) ListTags(req *metadatav1.ListTagsRequest, stream metadatav1.MetadataService_ListTagsServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix := req.GetRepoId() + ":"
	for k, digest := range s.tagToDigest {
		if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		name := k[len(prefix):]
		// honour the "last" cursor for pagination
		if req.GetLastName() != "" && name <= req.GetLastName() {
			continue
		}
		tag := &metadatav1.Tag{
			RepoId:         req.GetRepoId(),
			TenantId:       req.GetTenantId(),
			Name:           name,
			ManifestDigest: digest,
		}
		if err := stream.Send(tag); err != nil {
			return err
		}
	}
	return nil
}

func (s *mockMetadataServer) DeleteManifest(_ context.Context, req *metadatav1.DeleteManifestRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.manifests, req.GetRepoId()+":"+req.GetDigest())
	// Also remove any tag pointing to this digest.
	for k, d := range s.tagToDigest {
		if d == req.GetDigest() {
			delete(s.tagToDigest, k)
		}
	}
	return &emptypb.Empty{}, nil
}

func (s *mockMetadataServer) DeleteTag(_ context.Context, req *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tagToDigest, req.GetRepoId()+":"+req.GetName())
	return &emptypb.Empty{}, nil
}

func (s *mockMetadataServer) LinkBlob(_ context.Context, _ *metadatav1.LinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *mockMetadataServer) UnlinkBlob(_ context.Context, _ *metadatav1.UnlinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *mockMetadataServer) GetTenantQuotaUsage(_ context.Context, req *metadatav1.GetTenantQuotaUsageRequest) (*metadatav1.QuotaUsage, error) {
	// Return plenty of headroom so quota checks never block test pushes.
	return &metadatav1.QuotaUsage{
		TenantId:   req.GetTenantId(),
		UsedBytes:  0,
		QuotaBytes: 100 << 30, // 100 GiB
	}, nil
}

func (s *mockMetadataServer) IncrementTenantStorage(_ context.Context, _ *metadatav1.IncrementTenantStorageRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// mockStorageServer stores blobs in memory maps so push/pull can round-trip.
type mockStorageServer struct {
	storagev1.UnimplementedStorageServiceServer

	mu    sync.Mutex
	blobs map[string][]byte // key: storage key
}

func newMockStorageServer() *mockStorageServer {
	return &mockStorageServer{blobs: make(map[string][]byte)}
}

func (s *mockStorageServer) BlobExists(_ context.Context, req *storagev1.BlobExistsRequest) (*storagev1.BlobExistsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blobs[req.GetKey()]
	return &storagev1.BlobExistsResponse{Exists: ok}, nil
}

func (s *mockStorageServer) StatBlob(_ context.Context, req *storagev1.StatBlobRequest) (*storagev1.StatBlobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[req.GetKey()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "blob not found")
	}
	return &storagev1.StatBlobResponse{Size: int64(len(data)), ContentType: "application/octet-stream"}, nil
}

// PutBlob receives a client-streaming RPC: first message is Meta, subsequent are Chunk.
func (s *mockStorageServer) PutBlob(stream storagev1.StorageService_PutBlobServer) error {
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
		return status.Errorf(codes.InvalidArgument, "no key in put-blob request")
	}
	s.mu.Lock()
	s.blobs[key] = buf
	s.mu.Unlock()

	return stream.SendAndClose(&storagev1.PutBlobResponse{Size: int64(len(buf))})
}

// GetBlob streams blob data in 64 KiB chunks.
func (s *mockStorageServer) GetBlob(req *storagev1.GetBlobRequest, stream storagev1.StorageService_GetBlobServer) error {
	s.mu.Lock()
	data, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound, "blob not found")
	}

	const chunkSize = 64 * 1024
	for len(data) > 0 {
		n := len(data)
		if n > chunkSize {
			n = chunkSize
		}
		if err := stream.Send(&storagev1.GetBlobResponse{Chunk: data[:n]}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (s *mockStorageServer) DeleteBlob(_ context.Context, req *storagev1.DeleteBlobRequest) (*storagev1.DeleteBlobResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, req.GetKey())
	return &storagev1.DeleteBlobResponse{}, nil
}

// ── Test setup ────────────────────────────────────────────────────────────────

// coreEnv wraps the httptest.Server backed by the full core service stack
// and a shared bearer token that the mock auth server will accept.
type coreEnv struct {
	srv   *httptest.Server
	token string // any non-empty value is accepted by mockAuthServer
}

// newCoreEnv starts in-process gRPC servers for auth/metadata/storage, starts
// real Redis and RabbitMQ containers, wires up the registry service, and
// returns an httptest.Server ready to accept OCI requests.
func newCoreEnv(t *testing.T) *coreEnv {
	t.Helper()
	ctx := context.Background()

	// Real Redis for upload state + referrer store.
	redisAddr := containers.Redis(t)

	// Real RabbitMQ for push.completed events.
	amqpURL := containers.RabbitMQ(t)

	// ── in-process auth gRPC ─────────────────────────────────────────────────
	authLis := bufconn.Listen(bufSize)
	authSrv := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authSrv, &mockAuthServer{})
	go func() { _ = authSrv.Serve(authLis) }()
	t.Cleanup(authSrv.GracefulStop)

	// ── in-process metadata gRPC ─────────────────────────────────────────────
	metaLis := bufconn.Listen(bufSize)
	metaSrv := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaSrv, newMockMetadataServer())
	go func() { _ = metaSrv.Serve(metaLis) }()
	t.Cleanup(metaSrv.GracefulStop)

	// ── in-process storage gRPC ──────────────────────────────────────────────
	storageLis := bufconn.Listen(bufSize)
	storageSrv := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(storageSrv, newMockStorageServer())
	go func() { _ = storageSrv.Serve(storageLis) }()
	t.Cleanup(storageSrv.GracefulStop)

	// dialBufconn returns a *grpc.ClientConn connected to a bufconn listener.
	// bufconn.Listener.DialContext returns net.Conn, matching grpc.WithContextDialer's signature.
	dialBufconn := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient(
			"passthrough:///bufconn",
			grpc.WithContextDialer(lis.DialContext),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial bufconn: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	authConn := dialBufconn(authLis)
	metaConn := dialBufconn(metaLis)
	storageConn := dialBufconn(storageLis)

	// ── Redis client ─────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	// ── RabbitMQ publisher ───────────────────────────────────────────────────
	pub, err := publisher.New(amqpURL, events.ExchangeEvents)
	if err != nil {
		t.Fatalf("init rabbitmq publisher: %v", err)
	}
	t.Cleanup(pub.Close)

	// ── Service + handler ─────────────────────────────────────────────────────
	uploadStore := service.NewUploadStore(rdb)
	referrerStore := service.NewReferrerStore(rdb)
	authClient := service.NewAuthClient(authConn, rdb)
	// FE-API-042: sample rate 1.0 keeps the existing pull-path behaviour
	// observable by the integration test (every pull publishes a pull.image
	// event so the audit + metadata consumers always see traffic).
	registry := service.NewRegistry(metaConn, storageConn, uploadStore, referrerStore, pub, 1.0)

	h := handler.New(authClient, registry, "http://localhost/auth/token")
	mux := http.NewServeMux()
	h.Register(mux)

	return &coreEnv{
		srv:   httptest.NewServer(mux),
		token: "test-bearer-token",
	}
}

// get sends a GET request to the test server with the bearer token pre-set.
func (e *coreEnv) get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestVersionCheck verifies GET /v2/ returns 200 with a valid bearer token.
func TestVersionCheck(t *testing.T) {
	env := newCoreEnv(t)
	defer env.srv.Close()

	resp := env.get(t, "/v2/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

// TestVersionCheck_NoAuth verifies GET /v2/ returns 401 without credentials.
func TestVersionCheck_NoAuth(t *testing.T) {
	env := newCoreEnv(t)
	defer env.srv.Close()

	resp, err := http.Get(env.srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	// WWW-Authenticate header must be present for Docker clients.
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("expected WWW-Authenticate header")
	}
}

// TestBlobPushPull exercises a monolithic blob upload followed by a download.
// It uses the single-PUT path: POST /blobs/uploads/ → PUT /blobs/uploads/<uuid>?digest=...
func TestBlobPushPull(t *testing.T) {
	env := newCoreEnv(t)
	defer env.srv.Close()

	const repoName = "org/repo"
	blobData := []byte("hello integration test blob")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(blobData))

	// POST — initiate upload.
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/v2/"+repoName+"/blobs/uploads/", nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initiate upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("initiate upload: want 202, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("expected Location header from initiate upload")
	}

	// PUT — complete upload (monolithic: no PATCH before PUT).
	putURL := env.srv.URL + location + "?digest=" + digest
	putReq, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(blobData))
	putReq.Header.Set("Authorization", "Bearer "+env.token)
	putReq.Header.Set("Content-Type", "application/octet-stream")
	putReq.ContentLength = int64(len(blobData))
	resp2, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("complete upload: want 201, got %d", resp2.StatusCode)
	}

	// HEAD — blob must now exist.
	headReq, _ := http.NewRequest(http.MethodHead, env.srv.URL+"/v2/"+repoName+"/blobs/"+digest, nil)
	headReq.Header.Set("Authorization", "Bearer "+env.token)
	resp3, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("head blob: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("head blob: want 200, got %d", resp3.StatusCode)
	}

	// GET — pull the blob back and verify content.
	resp4 := env.get(t, "/v2/"+repoName+"/blobs/"+digest)
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("get blob: want 200, got %d", resp4.StatusCode)
	}
	body, _ := io.ReadAll(resp4.Body)
	if !bytes.Equal(body, blobData) {
		t.Fatalf("blob body mismatch: got %q want %q", body, blobData)
	}
}

// TestManifestPushPull pushes a minimal OCI manifest, then pulls it by tag and by digest.
func TestManifestPushPull(t *testing.T) {
	env := newCoreEnv(t)
	defer env.srv.Close()

	const (
		repoName = "org/myimage"
		tag      = "v1.0"
	)

	// Minimal OCI image manifest (no real blobs needed — the manifest is self-contained).
	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:44136fa355ba77b9ad7b1f9789cbe5f20f3bf249ed4c3d7eeee17a85e44c8c97",
			"size":      2,
		},
		"layers": []any{},
	}
	manifestBody, _ := json.Marshal(manifest)
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBody))

	// PUT manifest by tag.
	putReq, _ := http.NewRequest(http.MethodPut, env.srv.URL+"/v2/"+repoName+"/manifests/"+tag, bytes.NewReader(manifestBody))
	putReq.Header.Set("Authorization", "Bearer "+env.token)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("put manifest: want 201, got %d", putResp.StatusCode)
	}
	// Docker-Content-Digest header must be present.
	if putResp.Header.Get("Docker-Content-Digest") == "" {
		t.Fatal("expected Docker-Content-Digest header")
	}

	// GET by tag.
	resp := env.get(t, "/v2/"+repoName+"/manifests/"+tag)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get manifest by tag: want 200, got %d", resp.StatusCode)
	}

	// GET by digest.
	resp2 := env.get(t, "/v2/"+repoName+"/manifests/"+manifestDigest)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get manifest by digest: want 200, got %d", resp2.StatusCode)
	}

	// List tags — must include our tag.
	resp3 := env.get(t, "/v2/"+repoName+"/tags/list")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("list tags: want 200, got %d", resp3.StatusCode)
	}
	var tagList struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&tagList); err != nil {
		t.Fatalf("decode tag list: %v", err)
	}
	found := false
	for _, tg := range tagList.Tags {
		if tg == tag {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tag %q not found in tag list %v", tag, tagList.Tags)
	}
}

// TestManifestDelete verifies that a manifest can be deleted and is then unreachable.
func TestManifestDelete(t *testing.T) {
	env := newCoreEnv(t)
	defer env.srv.Close()

	const (
		repoName = "org/deleteme"
		tag      = "latest"
	)

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:44136fa355ba77b9ad7b1f9789cbe5f20f3bf249ed4c3d7eeee17a85e44c8c97", "size": 2},
		"layers":        []any{},
	}
	manifestBody, _ := json.Marshal(manifest)
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBody))

	// Push the manifest.
	putReq, _ := http.NewRequest(http.MethodPut, env.srv.URL+"/v2/"+repoName+"/manifests/"+tag, bytes.NewReader(manifestBody))
	putReq.Header.Set("Authorization", "Bearer "+env.token)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	resp.Body.Close()

	// Delete by digest (OCI spec: DELETE /manifests/<digest> deletes the manifest).
	delReq, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/v2/"+repoName+"/manifests/"+manifestDigest, nil)
	delReq.Header.Set("Authorization", "Bearer "+env.token)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete manifest: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete manifest: want 202, got %d", delResp.StatusCode)
	}

	// The manifest should now return 404.
	resp2 := env.get(t, "/v2/"+repoName+"/manifests/"+manifestDigest)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted manifest: want 404, got %d", resp2.StatusCode)
	}
}
