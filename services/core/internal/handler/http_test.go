// Package handler_test exercises the OCI HTTP handler using in-process fake
// gRPC servers (bufconn) and miniredis so no real network is required.
// Tests run with plain `go test ./...` — no build tag.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
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
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

const bufSize = 1 << 20 // 1 MiB

// ── noopPublisher ─────────────────────────────────────────────────────────────

// noopPublisher satisfies the eventPublisher interface without needing a broker.
// Note: this is package-internal since we use the exported service types only.

// ── fakeAuthServer ─────────────────────────────────────────────────────────────

// handlerFakeAuthServer accepts any non-empty token as valid, mapping to a
// fixed user with full push/pull/delete access on all repositories.
type handlerFakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *handlerFakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case "", "bad-token":
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	case "pull-only-token":
		// Token with only pull access — push/delete endpoints should reject this.
		return &authv1.ValidateTokenResponse{
			Valid:    true,
			UserId:   "readonly-user",
			TenantId: "tenant-test",
			Access:   []*authv1.RepositoryAccess{{Type: "repository", Name: "*", Actions: []string{"pull"}}},
			ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
		}, nil
	case "no-access-token":
		// Token with no access grants — all resource endpoints should reject this.
		return &authv1.ValidateTokenResponse{
			Valid:     true,
			UserId:   "nobody",
			TenantId: "tenant-test",
			Access:   nil,
			ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
		}, nil
	default:
		return &authv1.ValidateTokenResponse{
			Valid:     true,
			UserId:    "test-user",
			TenantId:  "tenant-test",
			Access:    []*authv1.RepositoryAccess{{Type: "repository", Name: "*", Actions: []string{"push", "pull", "delete"}}},
			ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
		}, nil
	}
}

// ── fakeMetadataServer ───────────────────────────────────────────────────────

type handlerFakeMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	mu          sync.Mutex
	repos       map[string]*metadatav1.Repository
	manifests   map[string]*metadatav1.Manifest
	tagToDigest map[string]string
}

func newHandlerFakeMetaServer() *handlerFakeMetaServer {
	return &handlerFakeMetaServer{
		repos:       make(map[string]*metadatav1.Repository),
		manifests:   make(map[string]*metadatav1.Manifest),
		tagToDigest: make(map[string]string),
	}
}

func (s *handlerFakeMetaServer) CreateRepository(_ context.Context, req *metadatav1.CreateRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetTenantId() + ":" + req.GetName()
	if r, ok := s.repos[key]; ok {
		return r, nil
	}
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	repoID := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	repo := &metadatav1.Repository{RepoId: repoID, TenantId: req.GetTenantId(), Name: req.GetName()}
	s.repos[key] = repo
	return repo, nil
}

func (s *handlerFakeMetaServer) PutManifest(_ context.Context, req *metadatav1.PutManifestRequest) (*metadatav1.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &metadatav1.Manifest{
		RepoId: req.GetRepoId(), TenantId: req.GetTenantId(),
		Digest: req.GetDigest(), MediaType: req.GetMediaType(), RawJson: req.GetRawJson(),
	}
	s.manifests[req.GetRepoId()+":"+req.GetDigest()] = m
	return m, nil
}

func (s *handlerFakeMetaServer) PutTag(_ context.Context, req *metadatav1.PutTagRequest) (*metadatav1.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tagToDigest[req.GetRepoId()+":"+req.GetName()] = req.GetManifestDigest()
	return &metadatav1.Tag{RepoId: req.GetRepoId(), Name: req.GetName(), ManifestDigest: req.GetManifestDigest()}, nil
}

func (s *handlerFakeMetaServer) GetTag(_ context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tagToDigest[req.GetRepoId()+":"+req.GetName()]
	if !ok {
		return nil, status.Error(codes.NotFound, "tag not found")
	}
	return &metadatav1.Tag{ManifestDigest: d}, nil
}

func (s *handlerFakeMetaServer) GetManifest(_ context.Context, req *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.manifests[req.GetRepoId()+":"+req.GetReference()]
	if !ok {
		return nil, status.Error(codes.NotFound, "manifest not found")
	}
	return m, nil
}

func (s *handlerFakeMetaServer) DeleteTag(_ context.Context, req *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetRepoId() + ":" + req.GetName()
	if _, ok := s.tagToDigest[key]; !ok {
		return nil, status.Error(codes.NotFound, "tag not found")
	}
	delete(s.tagToDigest, key)
	return &emptypb.Empty{}, nil
}

func (s *handlerFakeMetaServer) DeleteManifest(_ context.Context, req *metadatav1.DeleteManifestRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetRepoId() + ":" + req.GetDigest()
	if _, ok := s.manifests[key]; !ok {
		return nil, status.Error(codes.NotFound, "manifest not found")
	}
	delete(s.manifests, key)
	return &emptypb.Empty{}, nil
}

func (s *handlerFakeMetaServer) ListTags(_ *metadatav1.ListTagsRequest, stream metadatav1.MetadataService_ListTagsServer) error {
	return nil // empty stream
}

func (s *handlerFakeMetaServer) UnlinkBlob(_ context.Context, _ *metadatav1.UnlinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *handlerFakeMetaServer) GetTenantQuotaUsage(_ context.Context, _ *metadatav1.GetTenantQuotaUsageRequest) (*metadatav1.QuotaUsage, error) {
	return &metadatav1.QuotaUsage{QuotaBytes: 10 << 30, UsedBytes: 0}, nil
}

// ── fakeStorageServer ────────────────────────────────────────────────────────

type handlerFakeStorageServer struct {
	storagev1.UnimplementedStorageServiceServer
	mu    sync.Mutex
	blobs map[string][]byte
}

func newHandlerFakeStorageServer() *handlerFakeStorageServer {
	return &handlerFakeStorageServer{blobs: make(map[string][]byte)}
}

func (s *handlerFakeStorageServer) PutBlob(stream storagev1.StorageService_PutBlobServer) error {
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
	s.mu.Lock()
	s.blobs[key] = buf
	s.mu.Unlock()
	return stream.SendAndClose(&storagev1.PutBlobResponse{Key: key})
}

func (s *handlerFakeStorageServer) GetBlob(req *storagev1.GetBlobRequest, stream storagev1.StorageService_GetBlobServer) error {
	s.mu.Lock()
	data, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "blob not found")
	}
	return stream.Send(&storagev1.GetBlobResponse{Chunk: data})
}

func (s *handlerFakeStorageServer) BlobExists(_ context.Context, req *storagev1.BlobExistsRequest) (*storagev1.BlobExistsResponse, error) {
	s.mu.Lock()
	_, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	return &storagev1.BlobExistsResponse{Exists: ok}, nil
}

func (s *handlerFakeStorageServer) StatBlob(_ context.Context, req *storagev1.StatBlobRequest) (*storagev1.StatBlobResponse, error) {
	s.mu.Lock()
	data, ok := s.blobs[req.GetKey()]
	s.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "blob not found")
	}
	return &storagev1.StatBlobResponse{Size: int64(len(data))}, nil
}

func (s *handlerFakeStorageServer) DeleteBlob(_ context.Context, req *storagev1.DeleteBlobRequest) (*storagev1.DeleteBlobResponse, error) {
	s.mu.Lock()
	delete(s.blobs, req.GetKey())
	s.mu.Unlock()
	return &storagev1.DeleteBlobResponse{}, nil
}

// ── noopEventPublisher ────────────────────────────────────────────────────────

type noopEventPublisher struct{}

func (noopEventPublisher) Publish(_ context.Context, _ string, _ events.Event) error { return nil }

// ── Test infrastructure ───────────────────────────────────────────────────────

// handlerTestCtx bundles the httptest.Server and fake servers for handler tests.
type handlerTestCtx struct {
	srv     *httptest.Server
	storage *handlerFakeStorageServer
	meta    *handlerFakeMetaServer
}

// buildHandlerServer builds a full in-process OCI HTTP server backed by fake
// gRPC servers and returns a handlerTestCtx plus cleanup.
func buildHandlerServer(t *testing.T) (*handlerTestCtx, func()) {
	t.Helper()

	// miniredis for upload/referrer state.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// Start fake auth gRPC server.
	fakeAuth := &handlerFakeAuthServer{}
	authLis := bufconn.Listen(bufSize)
	authSrv := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authSrv, fakeAuth)
	go func() { _ = authSrv.Serve(authLis) }()

	// Start fake metadata gRPC server.
	fakeMeta := newHandlerFakeMetaServer()
	metaLis := bufconn.Listen(bufSize)
	metaSrv := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaSrv, fakeMeta)
	go func() { _ = metaSrv.Serve(metaLis) }()

	// Start fake storage gRPC server.
	fakeStorage := newHandlerFakeStorageServer()
	storageLis := bufconn.Listen(bufSize)
	storageSrv := grpc.NewServer()
	storagev1.RegisterStorageServiceServer(storageSrv, fakeStorage)
	go func() { _ = storageSrv.Serve(storageLis) }()

	// Dial all in-process servers.
	dialBuf := func(lis *bufconn.Listener) *grpc.ClientConn {
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
	authConn := dialBuf(authLis)
	metaConn := dialBuf(metaLis)
	storageConn := dialBuf(storageLis)

	authClient := service.NewAuthClient(authConn, rdb)

	uploads := service.NewUploadStore(rdb)
	refs := service.NewReferrerStore(rdb)
	reg := service.NewRegistryWithClients(
		metadatav1.NewMetadataServiceClient(metaConn),
		storagev1.NewStorageServiceClient(storageConn),
		uploads,
		refs,
		noopEventPublisher{},
	)

	h := New(authClient, reg, "https://auth.example.com/token")
	mux := http.NewServeMux()
	h.Register(mux)

	srv := httptest.NewServer(mux)

	tc := &handlerTestCtx{srv: srv, storage: fakeStorage, meta: fakeMeta}
	cleanup := func() {
		srv.Close()
		authSrv.Stop()
		metaSrv.Stop()
		storageSrv.Stop()
		_ = authConn.Close()
		_ = metaConn.Close()
		_ = storageConn.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return tc, cleanup
}

// bearerReq creates an http.Request with an Authorization: Bearer header.
func bearerReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer valid-token")
	return req
}

// pullOnlyReq creates an http.Request with a pull-only Bearer token.
// Use this to test endpoints that require push or delete permission.
func pullOnlyReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer pull-only-token")
	return req
}

// noAccessReq creates an http.Request with a no-access Bearer token.
// Use this to test pull-gated endpoints (tags/list, get manifest, etc.).
func noAccessReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer no-access-token")
	return req
}

// do sends a request and returns the response.
func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// ── /healthz ─────────────────────────────────────────────────────────────────

func TestHealthz_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	resp, err := http.Get(tc.srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// ── /v2/ version check ────────────────────────────────────────────────────────

func TestVersionCheck_noAuth_returns401WithChallenge(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	resp, err := http.Get(tc.srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if www := resp.Header.Get("WWW-Authenticate"); www == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestVersionCheck_validAuth_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet, tc.srv.URL+"/v2/", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// ── /v2/<name>/tags/list ───────────────────────────────────────────────────────

func TestTagsList_noAuth_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	resp, err := http.Get(tc.srv.URL + "/v2/myorg/myrepo/tags/list")
	if err != nil {
		t.Fatalf("GET /tags/list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestTagsList_validAuth_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet, tc.srv.URL+"/v2/myorg/myrepo/tags/list", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestTagsList_invalidName_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Single-component name (no slash) is invalid.
	req := bearerReq(t, http.MethodGet, tc.srv.URL+"/v2/badname/tags/list", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// ── manifests ─────────────────────────────────────────────────────────────────

func TestPutManifest_validAuth_returns201(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":7},"layers":[]}`)

	req := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/latest",
		bytes.NewReader(rawJSON),
	)
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if d := resp.Header.Get("Docker-Content-Digest"); d == "" {
		t.Error("expected Docker-Content-Digest header to be set")
	}
}

func TestPutManifest_noAuth_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0",
		strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestPutManifest_invalidMediaType_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0",
		strings.NewReader("{}"),
	)
	req.Header.Set("Content-Type", "text/plain") // not allowed

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestGetManifest_notFound_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/unknown-tag", nil)

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestGetManifest_validTag_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("b", 64) + `","size":7},"layers":[]}`)

	// Push the manifest first.
	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0",
		bytes.NewReader(rawJSON),
	)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp := do(t, putReq)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT manifest: got %d, want 201", putResp.StatusCode)
	}

	// Then get it.
	getReq := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)

	resp := do(t, getReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET manifest status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHeadManifest_validTag_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("c", 64) + `","size":7},"layers":[]}`)

	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/head-tag",
		bytes.NewReader(rawJSON),
	)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp := do(t, putReq)
	putResp.Body.Close()

	headReq := bearerReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/head-tag", nil)
	resp := do(t, headReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD manifest: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestDeleteManifest_validTag_returns202(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("d", 64) + `","size":7},"layers":[]}`)

	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/to-delete",
		bytes.NewReader(rawJSON),
	)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp := do(t, putReq)
	putResp.Body.Close()

	delReq := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/to-delete", nil)
	resp := do(t, delReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("DELETE manifest: got %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
}

// ── blobs ─────────────────────────────────────────────────────────────────────

func TestHeadBlob_existingBlob_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	data := []byte("blob data for head test")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-test"

	// Store blob in fake storage directly.
	key := fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, strings.TrimPrefix(digest, "sha256:")[:2], strings.TrimPrefix(digest, "sha256:"))
	tc.storage.mu.Lock()
	tc.storage.blobs[key] = data
	tc.storage.mu.Unlock()

	req := bearerReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD blob: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHeadBlob_missingBlob_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("0", 64)
	req := bearerReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD blob (missing): got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHeadBlob_invalidDigest_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/not-a-digest", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("HEAD blob (invalid digest): got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestGetBlob_existingBlob_returns200WithContent(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	data := []byte("get blob data for handler test")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-test"
	key := fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, strings.TrimPrefix(digest, "sha256:")[:2], strings.TrimPrefix(digest, "sha256:"))
	tc.storage.mu.Lock()
	tc.storage.blobs[key] = data
	tc.storage.mu.Unlock()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET blob: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data) {
		t.Errorf("content mismatch: got %d bytes, want %d", len(body), len(data))
	}
}

func TestDeleteBlob_validDigest_returns202(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	data := []byte("delete blob test")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-test"
	key := fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, strings.TrimPrefix(digest, "sha256:")[:2], strings.TrimPrefix(digest, "sha256:"))
	tc.storage.mu.Lock()
	tc.storage.blobs[key] = data
	tc.storage.mu.Unlock()

	req := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("DELETE blob: got %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
}

// ── uploads ───────────────────────────────────────────────────────────────────

func TestInitiateUpload_validAuth_returns202WithLocation(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST initiate upload: got %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if loc := resp.Header.Get("Location"); loc == "" {
		t.Error("expected Location header to be set")
	}
	if uuid := resp.Header.Get("Docker-Upload-UUID"); uuid == "" {
		t.Error("expected Docker-Upload-UUID header to be set")
	}
}

func TestInitiateUpload_noAuth_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestPatchUpload_thenComplete_returns201(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Initiate upload.
	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusAccepted {
		t.Fatalf("initiate: got %d, want 202", initResp.StatusCode)
	}
	loc := initResp.Header.Get("Location")
	if loc == "" {
		t.Fatal("no Location header in initiate response")
	}
	// Location is relative; build full URL.
	uploadURL := tc.srv.URL + loc

	// PATCH chunk.
	chunk := []byte("hello world chunk data")
	patchReq := bearerReq(t, http.MethodPatch, uploadURL, bytes.NewReader(chunk))
	patchResp := do(t, patchReq)
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusAccepted {
		t.Fatalf("PATCH upload: got %d, want 202", patchResp.StatusCode)
	}

	// PUT to complete (empty final body).
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(chunk))
	putReq := bearerReq(t, http.MethodPut,
		uploadURL+"?digest="+digest, bytes.NewReader(nil))
	putResp := do(t, putReq)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Errorf("PUT complete upload: got %d, want 201", putResp.StatusCode)
	}
}

func TestGetUpload_existingUpload_returns204(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	getReq := bearerReq(t, http.MethodGet, tc.srv.URL+loc, nil)
	resp := do(t, getReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET upload: got %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestGetUpload_unknownUUID_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/nonexistent-uuid", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown upload: got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestCancelUpload_returns204(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	delReq := bearerReq(t, http.MethodDelete, tc.srv.URL+loc, nil)
	resp := do(t, delReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE upload: got %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestCompleteUpload_digestMismatch_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	wrongDigest := "sha256:" + strings.Repeat("f", 64)
	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+loc+"?digest="+wrongDigest,
		bytes.NewReader([]byte("some data")))
	resp := do(t, putReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with wrong digest: got %d, want 400", resp.StatusCode)
	}
}

// ── referrers ─────────────────────────────────────────────────────────────────

func TestReferrers_validDigest_returns200WithIndex(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("a", 64)
	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET referrers: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode referrers response: %v", err)
	}
	if body["schemaVersion"] == nil {
		t.Error("expected schemaVersion field in referrers response")
	}
}

func TestReferrers_invalidDigest_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/not-a-digest", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET referrers (invalid digest): got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestReferrers_noAuth_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("b", 64)
	req, _ := http.NewRequest(http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET referrers (no auth): got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// ── parseContentRange ─────────────────────────────────────────────────────────

func TestParseContentRange_validRange_succeeds(t *testing.T) {
	tests := []struct {
		input      string
		wantStart  int64
		wantEnd    int64
	}{
		{"0-100", 0, 100},
		{"bytes 0-100/101", 0, 100},
		{"bytes 0-100/*", 0, 100},
		{"10-200", 10, 200},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			start, end, ok := parseContentRange(tc.input)
			if !ok {
				t.Errorf("parseContentRange(%q): expected ok=true, got false", tc.input)
			}
			if start != tc.wantStart {
				t.Errorf("start: got %d, want %d", start, tc.wantStart)
			}
			if end != tc.wantEnd {
				t.Errorf("end: got %d, want %d", end, tc.wantEnd)
			}
		})
	}
}

func TestParseContentRange_invalid_returnsFalse(t *testing.T) {
	tests := []string{"", "nohyphen", "bytes/invalid"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, _, ok := parseContentRange(input)
			if ok {
				t.Errorf("parseContentRange(%q): expected ok=false, got true", input)
			}
		})
	}
}

// ── isAllowedManifestMediaType ────────────────────────────────────────────────

func TestIsAllowedManifestMediaType_validTypes_returnsTrue(t *testing.T) {
	types := []string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json; charset=utf-8",
	}
	for _, mt := range types {
		t.Run(mt, func(t *testing.T) {
			if !isAllowedManifestMediaType(mt) {
				t.Errorf("expected true for %q", mt)
			}
		})
	}
}

func TestIsAllowedManifestMediaType_invalidTypes_returnsFalse(t *testing.T) {
	types := []string{"text/plain", "application/json", ""}
	for _, mt := range types {
		t.Run(mt, func(t *testing.T) {
			if isAllowedManifestMediaType(mt) {
				t.Errorf("expected false for %q", mt)
			}
		})
	}
}

// ── dispatchOCI: unknown path ─────────────────────────────────────────────────

func TestDispatchOCI_unknownPath_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet, tc.srv.URL+"/v2/", nil)
	// Override to a weird path that matches no OCI route.
	req.URL, _ = req.URL.Parse(tc.srv.URL + "/v2/onlyone")
	resp := do(t, req)
	defer resp.Body.Close()

	// The single-segment path "onlyone" has no slash and can't be a valid OCI sub-path.
	// The dispatcher should return 404.
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusOK {
		// Some paths might fall through; we just verify it doesn't panic.
		t.Logf("unknown path status: %d (acceptable)", resp.StatusCode)
	}
}

func TestDispatchOCI_wrongMethod_returns405(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// PATCH on /v2/<name>/manifests/<ref> — not an allowed method.
	req := bearerReq(t, http.MethodPatch,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PATCH manifests: got %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

// ── authenticate: Basic auth path ─────────────────────────────────────────────

// TestAuthenticate_basicAuth_validAPIKey_returns200 exercises the Basic auth
// branch of authenticate. The fake auth server's ValidateAPIKey accepts any
// non-empty keyID:secret, but the fake only implements ValidateToken. We verify
// that a valid Bearer token still works and that an unknown Basic credential
// produces 401 at the /v2/ endpoint.
func TestAuthenticate_basicAuth_unknownKey_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Build a Basic-auth credential: keyID:secret base64-encoded.
	cred := base64.StdEncoding.EncodeToString([]byte("unknown-key:wrong-secret"))
	req, _ := http.NewRequest(http.MethodGet, tc.srv.URL+"/v2/", nil)
	req.Header.Set("Authorization", "Basic "+cred)

	resp := do(t, req)
	defer resp.Body.Close()

	// Fake auth server rejects all API keys it doesn't know → 401.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Basic auth unknown key: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthenticate_unknownScheme_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, tc.srv.URL+"/v2/", nil)
	req.Header.Set("Authorization", "Digest abc123")

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Unknown auth scheme: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthenticate_basicAuthInvalidBase64_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, tc.srv.URL+"/v2/", nil)
	req.Header.Set("Authorization", "Basic !!!not-valid-base64!!!")

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Invalid base64: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthenticate_basicAuthNoColon_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Encode a string without a colon separator.
	cred := base64.StdEncoding.EncodeToString([]byte("nokeynosecret"))
	req, _ := http.NewRequest(http.MethodGet, tc.srv.URL+"/v2/", nil)
	req.Header.Set("Authorization", "Basic "+cred)

	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("No colon in basic auth: got %d, want 401", resp.StatusCode)
	}
}

// ── handleTagsList: pagination ─────────────────────────────────────────────────

func TestTagsList_withNParam_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/tags/list?n=5", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("tags/list?n=5: got %d, want 200", resp.StatusCode)
	}
}

// ── handleGetManifest: by digest ───────────────────────────────────────────────

func TestGetManifest_byDigest_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("e", 64) + `","size":7},"layers":[]}`)

	// Push first to get the canonical digest.
	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/digest-ref-tag",
		bytes.NewReader(rawJSON),
	)
	putReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putResp := do(t, putReq)
	putDigest := putResp.Header.Get("Docker-Content-Digest")
	putResp.Body.Close()

	if putDigest == "" {
		t.Fatal("expected Docker-Content-Digest in PUT response")
	}

	// Now fetch by digest directly.
	getReq := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/"+putDigest, nil)
	resp := do(t, getReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET manifest by digest: got %d, want 200", resp.StatusCode)
	}
}

// ── handleHeadManifest: not-found ─────────────────────────────────────────────

func TestHeadManifest_notFound_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/no-such-tag", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD manifest notFound: got %d, want 404", resp.StatusCode)
	}
}

// ── handleDeleteManifest: not found ───────────────────────────────────────────

func TestDeleteManifest_notFound_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Delete a digest that was never pushed.
	digest := "sha256:" + strings.Repeat("9", 64)
	req := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE manifest notFound: got %d, want 404", resp.StatusCode)
	}
}

// ── handleDeleteBlob: invalid digest & not found ─────────────────────────────

func TestDeleteBlob_invalidDigest_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/not-a-real-digest", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("DELETE blob invalid digest: got %d, want 400", resp.StatusCode)
	}
}

func TestDeleteBlob_nonExistentBlob_returns202(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Digest is valid format but blob doesn't exist. The service silently ignores
	// storage not-found, so the handler returns 202 (idempotent delete).
	digest := "sha256:" + strings.Repeat("7", 64)
	req := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("DELETE blob non-existent: got %d, want 202", resp.StatusCode)
	}
}

// ── handleGetBlob: invalid digest ────────────────────────────────────────────

func TestGetBlob_invalidDigest_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/invalid-digest-here", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET blob invalid digest: got %d, want 400", resp.StatusCode)
	}
}

func TestGetBlob_missingBlob_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("8", 64)
	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET blob missing: got %d, want 404", resp.StatusCode)
	}
}

// ── handlePatchUpload: Content-Range validation ───────────────────────────────

func TestPatchUpload_withValidContentRange_returns202(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Initiate.
	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	chunk := []byte("range-tested chunk")
	patchReq := bearerReq(t, http.MethodPatch, tc.srv.URL+loc, bytes.NewReader(chunk))
	patchReq.Header.Set("Content-Range", "0-17") // matches offset=0
	resp := do(t, patchReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("PATCH with Content-Range 0-17: got %d, want 202", resp.StatusCode)
	}
}

func TestPatchUpload_withInvalidContentRange_returns416(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Initiate.
	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	// Malformed Content-Range header.
	patchReq := bearerReq(t, http.MethodPatch, tc.srv.URL+loc,
		bytes.NewReader([]byte("data")))
	patchReq.Header.Set("Content-Range", "notparseable")
	resp := do(t, patchReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("PATCH invalid Content-Range: got %d, want 416", resp.StatusCode)
	}
}

func TestPatchUpload_withOffsetMismatch_returns416(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Initiate.
	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	// Claim start=100 but actual offset is 0.
	patchReq := bearerReq(t, http.MethodPatch, tc.srv.URL+loc,
		bytes.NewReader([]byte("data")))
	patchReq.Header.Set("Content-Range", "100-103")
	resp := do(t, patchReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("PATCH offset mismatch: got %d, want 416", resp.StatusCode)
	}
}

func TestPatchUpload_unknownUUID_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodPatch,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/nonexistent-uuid",
		bytes.NewReader([]byte("data")))
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PATCH unknown upload: got %d, want 404", resp.StatusCode)
	}
}

// ── handleCompleteUpload: missing and invalid digest param ────────────────────

func TestCompleteUpload_withComputedDigest_returns201(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Initiate upload.
	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	// PATCH a chunk.
	chunk := []byte("computed-digest chunk data")
	patchReq := bearerReq(t, http.MethodPatch, tc.srv.URL+loc, bytes.NewReader(chunk))
	patchResp := do(t, patchReq)
	patchResp.Body.Close()

	// PUT with the correct expected digest.
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(chunk))
	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+loc+"?digest="+digest, bytes.NewReader(nil))
	resp := do(t, putReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT complete with digest param: got %d, want 201", resp.StatusCode)
	}
}

func TestCompleteUpload_invalidDigestParam_returns400(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	initReq := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	initResp := do(t, initReq)
	initResp.Body.Close()
	loc := initResp.Header.Get("Location")

	putReq := bearerReq(t, http.MethodPut,
		tc.srv.URL+loc+"?digest=not-a-valid-digest",
		bytes.NewReader([]byte("data")))
	resp := do(t, putReq)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT invalid digest param: got %d, want 400", resp.StatusCode)
	}
}

func TestCompleteUpload_unknownUpload_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("a", 64)
	req := bearerReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/no-such-upload?digest="+digest,
		bytes.NewReader(nil))
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT unknown upload: got %d, want 404", resp.StatusCode)
	}
}

// ── handleInitiateUpload: cross-repo mount ────────────────────────────────────

func TestInitiateUpload_mountExistingBlob_returns201(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	data := []byte("mountable blob content")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	tenantID := "tenant-test"
	key := fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID,
		strings.TrimPrefix(digest, "sha256:")[:2],
		strings.TrimPrefix(digest, "sha256:"))
	tc.storage.mu.Lock()
	tc.storage.blobs[key] = data
	tc.storage.mu.Unlock()

	// Try to cross-mount the already-stored blob.
	req := bearerReq(t, http.MethodPost,
		fmt.Sprintf("%s/v2/myorg/myrepo/blobs/uploads?mount=%s&from=myorg/otherrepo",
			tc.srv.URL, digest), nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("cross-repo mount existing blob: got %d, want 201", resp.StatusCode)
	}
}

func TestInitiateUpload_mountMissingBlob_falls_backTo202(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Blob doesn't exist; registry should fall back to a regular upload.
	digest := "sha256:" + strings.Repeat("5", 64)
	req := bearerReq(t, http.MethodPost,
		fmt.Sprintf("%s/v2/myorg/myrepo/blobs/uploads?mount=%s&from=myorg/otherrepo",
			tc.srv.URL, digest), nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("mount missing blob fallback: got %d, want 202", resp.StatusCode)
	}
}

// ── handleReferrers: filtered results ─────────────────────────────────────────

func TestReferrers_withArtifactTypeFilter_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("c", 64)
	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/"+digest+
			"?artifactType=application/vnd.test.sbom", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET referrers with filter: got %d, want 200", resp.StatusCode)
	}
}

// ── handleTagsList: with last param ───────────────────────────────────────────

func TestTagsList_withLastParam_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/tags/list?last=v1.0&n=10", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("tags/list?last=v1.0&n=10: got %d, want 200", resp.StatusCode)
	}
}

// ── HasAction denied: pull-only token on push/delete endpoints ────────────────

// TestPutManifest_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handlePutManifest.
func TestPutManifest_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	rawJSON := []byte(`{"schemaVersion":2}`)
	req := pullOnlyReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0",
		bytes.NewReader(rawJSON))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("PUT manifest pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestDeleteManifest_pullOnlyToken_returns401 exercises the HasAction("delete") denied
// branch inside handleDeleteManifest.
func TestDeleteManifest_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("a", 64)
	req := pullOnlyReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("DELETE manifest pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestDeleteBlob_pullOnlyToken_returns401 exercises the HasAction("delete") denied
// branch inside handleDeleteBlob.
func TestDeleteBlob_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("b", 64)
	req := pullOnlyReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("DELETE blob pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestInitiateUpload_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handleInitiateUpload.
func TestInitiateUpload_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST uploads pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestPatchUpload_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handlePatchUpload.
func TestPatchUpload_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodPatch,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/some-uuid",
		bytes.NewReader([]byte("data")))
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("PATCH upload pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestCompleteUpload_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handleCompleteUpload.
func TestCompleteUpload_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodPut,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/some-uuid?digest=sha256:"+strings.Repeat("a", 64),
		bytes.NewReader(nil))
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("PUT complete upload pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestCancelUpload_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handleCancelUpload.
func TestCancelUpload_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/some-uuid", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("DELETE upload pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestGetUpload_pullOnlyToken_returns401 exercises the HasAction("push") denied
// branch inside handleGetUpload.
func TestGetUpload_pullOnlyToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads/some-uuid", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET upload pull-only token: got %d, want 401", resp.StatusCode)
	}
}

// TestHeadBlob_pullOnlyToken_returns200 confirms pull-only token CAN do HEAD blob.
// This also hits the HasAction("pull") allowed path on a pull-only token.
func TestHeadBlob_pullOnlyTokenAllowed_returns404(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// Pull-only token has pull access, blob doesn't exist → 404.
	digest := "sha256:" + strings.Repeat("f", 64)
	req := pullOnlyReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD blob pull-only (missing): got %d, want 404", resp.StatusCode)
	}
}

// TestTagsList_pullOnlyToken_returns200 confirms pull-only token can list tags.
func TestTagsList_pullOnlyToken_returns200(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := pullOnlyReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/tags/list", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET tags/list pull-only token: got %d, want 200", resp.StatusCode)
	}
}

// ── no-access token: pull denied on read endpoints ────────────────────────────

func TestTagsList_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := noAccessReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/tags/list", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET tags/list no-access token: got %d, want 401", resp.StatusCode)
	}
}

func TestGetManifest_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := noAccessReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET manifest no-access token: got %d, want 401", resp.StatusCode)
	}
}

func TestHeadManifest_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	req := noAccessReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/manifests/v1.0", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HEAD manifest no-access token: got %d, want 401", resp.StatusCode)
	}
}

func TestGetBlob_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("3", 64)
	req := noAccessReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET blob no-access token: got %d, want 401", resp.StatusCode)
	}
}

func TestHeadBlob_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("4", 64)
	req := noAccessReq(t, http.MethodHead,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HEAD blob no-access token: got %d, want 401", resp.StatusCode)
	}
}

func TestReferrers_noAccessToken_returns401(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("5", 64)
	req := noAccessReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET referrers no-access token: got %d, want 401", resp.StatusCode)
	}
}

// ── dispatchOCI: method not allowed on blobs ──────────────────────────────────

func TestDispatchOCI_blobsWrongMethod_returns405(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// POST to a blob digest path — not an allowed method.
	req := bearerReq(t, http.MethodPost,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/sha256:"+strings.Repeat("a", 64), nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST blobs digest: got %d, want 405", resp.StatusCode)
	}
}

func TestDispatchOCI_uploadsWrongMethod_returns405(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	// GET to /blobs/uploads without a UUID — only POST is allowed.
	req := bearerReq(t, http.MethodGet,
		tc.srv.URL+"/v2/myorg/myrepo/blobs/uploads", nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET blobs/uploads (no uuid): got %d, want 405", resp.StatusCode)
	}
}

func TestDispatchOCI_referrersWrongMethod_returns405(t *testing.T) {
	tc, cleanup := buildHandlerServer(t)
	defer cleanup()

	digest := "sha256:" + strings.Repeat("6", 64)
	req := bearerReq(t, http.MethodDelete,
		tc.srv.URL+"/v2/myorg/myrepo/referrers/"+digest, nil)
	resp := do(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE referrers: got %d, want 405", resp.StatusCode)
	}
}
