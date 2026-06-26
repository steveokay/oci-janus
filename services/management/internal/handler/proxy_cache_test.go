// FUT-013 — proxy_cache_test.go covers the three new
// /api/v1/proxy/cache* routes. Mirrors the admin_gc_test.go bufconn
// pattern: in-process gRPC fakes wired through the real management
// mux, HTTP round-trip + status assertions.
//
// Coverage:
//   - h.proxy == nil → 404 (PROXY_GRPC_ADDR unset path)
//   - non-workspace-admin → 403 (readerToken)
//   - workspace-admin happy path on list + stats + delete
//   - delete NotFound → 404
//   - list propagates page_token + filters to the gRPC call
package handler_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ─── Fake ProxyService ──────────────────────────────────────────────

type fakeProxyServer struct {
	proxyv1.UnimplementedProxyServiceServer

	mu sync.Mutex

	listReturn *proxyv1.ListCachedManifestsResponse
	listErr    error
	lastList   *proxyv1.ListCachedManifestsRequest

	statsReturn *proxyv1.CacheStats
	statsErr    error

	deleteErr    error
	lastDeleteID string

	// FUT-016 — GetCachedManifest fake state.
	getReturn *proxyv1.CachedManifestDetail
	getErr    error
	lastGetID string
}

func (s *fakeProxyServer) ListCachedManifests(_ context.Context, req *proxyv1.ListCachedManifestsRequest) (*proxyv1.ListCachedManifestsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastList = req
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listReturn != nil {
		return s.listReturn, nil
	}
	return &proxyv1.ListCachedManifestsResponse{}, nil
}

func (s *fakeProxyServer) GetCacheStats(_ context.Context, _ *proxyv1.GetCacheStatsRequest) (*proxyv1.CacheStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	if s.statsReturn != nil {
		return s.statsReturn, nil
	}
	return &proxyv1.CacheStats{}, nil
}

func (s *fakeProxyServer) GetCachedManifest(_ context.Context, req *proxyv1.GetCachedManifestRequest) (*proxyv1.CachedManifestDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGetID = req.GetId()
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.getReturn != nil {
		return s.getReturn, nil
	}
	return &proxyv1.CachedManifestDetail{}, nil
}

func (s *fakeProxyServer) DeleteCachedManifest(_ context.Context, req *proxyv1.DeleteCachedManifestRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDeleteID = req.GetId()
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	return &emptypb.Empty{}, nil
}

// newProxyEnv stands up the bufconn stack with the proxy client wired.
// Auth uses the default fakeAuthServer (admin/writer/reader/owner
// tokens defined in handler_test.go), so the workspace-admin gate
// (requireDomainAdmin: admin/owner on any org) flips based on token.
func newProxyEnv(t *testing.T) (*testEnv, *fakeProxyServer) {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	fakeProxy := &fakeProxyServer{}
	proxyLis := bufconn.Listen(bufSize)
	proxyGRPC := grpc.NewServer()
	proxyv1.RegisterProxyServiceServer(proxyGRPC, fakeProxy)
	healthpb.RegisterHealthServer(proxyGRPC, &fakeHealthServer{})
	go func() { _ = proxyGRPC.Serve(proxyLis) }()
	t.Cleanup(proxyGRPC.Stop)

	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial bufconn: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil, // publisher not exercised
		"",
		healthpb.NewHealthClient(dial(authLis)),
	).WithProxyClient(proxyv1.NewProxyServiceClient(dial(proxyLis)))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeProxy
}

// ─── Tests ──────────────────────────────────────────────────────────

// TestProxyCache_Disabled_returns404 — when h.proxy is nil (no
// PROXY_GRPC_ADDR), every route 404s. This is what makes the FE's
// "probe + hide sidebar entry" pattern work.
func TestProxyCache_Disabled_returns404(t *testing.T) {
	env := newTestEnv(t) // no WithProxyClient
	for _, path := range []string{
		"/api/v1/proxy/cache",
		"/api/v1/proxy/cache/stats",
		// FUT-016 — detail route also 404s when proxy client is unwired.
		"/api/v1/proxy/cache/00000000-0000-0000-0000-000000000001",
	} {
		resp := env.get(t, path, platformAdminToken)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, resp.StatusCode)
		}
	}
	// DELETE — must also 404 before the RBAC gate fires.
	resp := env.del(t, "/api/v1/proxy/cache/00000000-0000-0000-0000-000000000001", platformAdminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete on disabled route: expected 404, got %d", resp.StatusCode)
	}
}

// TestProxyCache_NonAdmin_returns403 — readerToken hits the
// requireDomainAdmin gate and bounces with 403. Confirms the RBAC
// gate fires AFTER the route-disabled check (we wired proxy here).
func TestProxyCache_NonAdmin_returns403(t *testing.T) {
	env, _ := newProxyEnv(t)
	for _, path := range []string{
		"/api/v1/proxy/cache",
		"/api/v1/proxy/cache/stats",
		// FUT-016 — detail route is workspace-admin gated too.
		"/api/v1/proxy/cache/00000000-0000-0000-0000-000000000001",
	} {
		resp := env.get(t, path, readerToken)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: expected 403 for reader, got %d", path, resp.StatusCode)
		}
	}
}

// TestProxyCacheList_HappyPath asserts the proto→JSON shape: every
// CachedManifest field maps; last_pulled_at OMITTED when nil;
// next_page_token round-trips.
func TestProxyCacheList_HappyPath(t *testing.T) {
	env, fake := newProxyEnv(t)
	fetched := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	pulled := fetched.Add(2 * time.Hour)

	fake.listReturn = &proxyv1.ListCachedManifestsResponse{
		Manifests: []*proxyv1.CachedManifest{
			{
				Id:           "11111111-1111-4111-8111-111111111111",
				UpstreamId:   "22222222-2222-4222-8222-222222222222",
				UpstreamName: "dockerhub",
				Image:        "library/alpine",
				Reference:    "3.20",
				Digest:       "sha256:abcd",
				MediaType:    "application/vnd.docker.distribution.manifest.v2+json",
				SizeBytes:    1024,
				FetchedAt:    timestamppb.New(fetched),
				LastPulledAt: timestamppb.New(pulled),
				PullCount:    7,
			},
			{
				// Never pulled — LastPulledAt unset; the JSON must omit
				// the field rather than emit a zero-time.
				Id:           "33333333-3333-4333-8333-333333333333",
				UpstreamId:   "22222222-2222-4222-8222-222222222222",
				UpstreamName: "dockerhub",
				Image:        "library/busybox",
				Reference:    "latest",
				Digest:       "sha256:beef",
				MediaType:    "application/vnd.docker.distribution.manifest.v2+json",
				SizeBytes:    512,
				FetchedAt:    timestamppb.New(fetched),
				PullCount:    0,
			},
		},
		NextPageToken: "next-page-token-blob",
	}

	resp := env.get(t, "/api/v1/proxy/cache?page_size=2&image_contains=lib", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Manifests []struct {
			ID           string  `json:"id"`
			UpstreamName string  `json:"upstream_name"`
			Image        string  `json:"image"`
			SizeBytes    int64   `json:"size_bytes"`
			PullCount    int64   `json:"pull_count"`
			LastPulledAt *string `json:"last_pulled_at,omitempty"`
		} `json:"manifests"`
		NextPageToken string `json:"next_page_token,omitempty"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Manifests) != 2 {
		t.Fatalf("manifests: got %d, want 2", len(body.Manifests))
	}
	if body.Manifests[0].LastPulledAt == nil {
		t.Error("row 0: last_pulled_at should be present")
	}
	if body.Manifests[1].LastPulledAt != nil {
		t.Errorf("row 1: last_pulled_at should be omitted (never pulled), got %q", *body.Manifests[1].LastPulledAt)
	}
	if body.NextPageToken != "next-page-token-blob" {
		t.Errorf("next_page_token: got %q", body.NextPageToken)
	}

	// Confirm filters propagate to the upstream gRPC call.
	if fake.lastList.GetPageSize() != 2 {
		t.Errorf("page_size: got %d", fake.lastList.GetPageSize())
	}
	if fake.lastList.GetImageContains() != "lib" {
		t.Errorf("image_contains: got %q", fake.lastList.GetImageContains())
	}
}

// TestProxyCacheList_BadPageSize — 1..100 only. 0 means "default"
// (not a validation failure) but negative and >100 should 400.
func TestProxyCacheList_BadPageSize(t *testing.T) {
	env, _ := newProxyEnv(t)
	resp := env.get(t, "/api/v1/proxy/cache?page_size=999", platformAdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("page_size=999: expected 400, got %d", resp.StatusCode)
	}
}

// TestProxyCacheStats_HappyPath — fields map straight through.
func TestProxyCacheStats_HappyPath(t *testing.T) {
	env, fake := newProxyEnv(t)
	fake.statsReturn = &proxyv1.CacheStats{
		TotalManifests:  42,
		TotalBytes:      8 * 1024 * 1024,
		UniqueUpstreams: 2,
		TotalPulls:      117,
	}
	resp := env.get(t, "/api/v1/proxy/cache/stats", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		TotalManifests  int64 `json:"total_manifests"`
		TotalBytes      int64 `json:"total_bytes"`
		UniqueUpstreams int64 `json:"unique_upstreams"`
		TotalPulls      int64 `json:"total_pulls"`
	}
	decodeJSON(t, resp, &body)
	if body.TotalManifests != 42 || body.TotalPulls != 117 || body.UniqueUpstreams != 2 {
		t.Errorf("stats: %+v", body)
	}
}

// TestProxyCacheEvict_HappyPath — 204 on successful delete; the id
// propagates to the upstream RPC call.
func TestProxyCacheEvict_HappyPath(t *testing.T) {
	env, fake := newProxyEnv(t)
	target := "44444444-4444-4444-8444-444444444444"
	resp := env.del(t, "/api/v1/proxy/cache/"+target, platformAdminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if fake.lastDeleteID != target {
		t.Errorf("delete id: got %q, want %q", fake.lastDeleteID, target)
	}
}

// TestProxyCacheEvict_NotFound — gRPC NotFound maps to HTTP 404.
func TestProxyCacheEvict_NotFound(t *testing.T) {
	env, fake := newProxyEnv(t)
	fake.deleteErr = status.Error(codes.NotFound, "cached manifest not found")
	resp := env.del(t, "/api/v1/proxy/cache/55555555-5555-4555-8555-555555555555", platformAdminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestProxyCacheList_GRPCInvalidArg — InvalidArgument from the proxy
// (e.g. malformed page_token) surfaces as 400 to the client.
func TestProxyCacheList_GRPCInvalidArg(t *testing.T) {
	env, fake := newProxyEnv(t)
	fake.listErr = status.Error(codes.InvalidArgument, "bad page_token")
	resp := env.get(t, "/api/v1/proxy/cache?page_token=garbage", platformAdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	buf, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(buf), "bad page_token") {
		t.Errorf("error body should include grpc message, got %q", string(buf))
	}
}

// ─── FUT-016 detail-route tests ────────────────────────────────────────────

// imageManifestBody is a minimal OCI image manifest used by the detail-route
// happy-path test. Captures the projection the BFF must produce: one config
// + two layers. Keeping this inline (vs a testdata file) makes the
// expectation obvious next to the assertions.
const imageManifestBody = `{
	"schemaVersion": 2,
	"mediaType": "application/vnd.oci.image.manifest.v1+json",
	"config": {
		"mediaType": "application/vnd.oci.image.config.v1+json",
		"digest": "sha256:c0nf1g",
		"size": 1234
	},
	"layers": [
		{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest": "sha256:1ayer0ne",
			"size": 100
		},
		{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest": "sha256:1ayertwo",
			"size": 200
		}
	]
}`

// indexManifestBody is a minimal OCI image index used to confirm the
// detail-route branches on `manifests[]` presence (not media type) and
// emits `kind: "index"` + `manifests[]`.
const indexManifestBody = `{
	"schemaVersion": 2,
	"mediaType": "application/vnd.oci.image.index.v1+json",
	"manifests": [
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest": "sha256:amd64manifest",
			"size": 500,
			"platform": {"architecture": "amd64", "os": "linux"}
		},
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"digest": "sha256:arm64manifest",
			"size": 600,
			"platform": {"architecture": "arm64", "os": "linux", "variant": "v8"}
		}
	]
}`

// TestProxyCacheDetail_ImageManifest_HappyPath — workspace-admin gets the
// full detail row, kind=image, parsed layers, body base64-roundtrips.
func TestProxyCacheDetail_ImageManifest_HappyPath(t *testing.T) {
	env, fake := newProxyEnv(t)
	fetched := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	target := "11111111-1111-4111-8111-111111111111"

	fake.getReturn = &proxyv1.CachedManifestDetail{
		Manifest: &proxyv1.CachedManifest{
			Id:           target,
			UpstreamId:   "22222222-2222-4222-8222-222222222222",
			UpstreamName: "dockerhub",
			Image:        "library/alpine",
			Reference:    "3.20",
			Digest:       "sha256:abcd",
			MediaType:    "application/vnd.oci.image.manifest.v1+json",
			SizeBytes:    int64(len(imageManifestBody)),
			FetchedAt:    timestamppb.New(fetched),
			PullCount:    3,
		},
		Body: []byte(imageManifestBody),
	}

	resp := env.get(t, "/api/v1/proxy/cache/"+target, platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Image      string `json:"image"`
		BodyBase64 string `json:"body_base64"`
		Layers     []struct {
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
			MediaType string `json:"media_type"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	decodeJSON(t, resp, &body)

	if body.ID != target {
		t.Errorf("id: got %q, want %q", body.ID, target)
	}
	if body.Kind != "image" {
		t.Errorf("kind: got %q, want %q", body.Kind, "image")
	}
	if body.Image != "library/alpine" {
		t.Errorf("image: got %q", body.Image)
	}
	if len(body.Layers) != 2 {
		t.Fatalf("layers: got %d, want 2", len(body.Layers))
	}
	if body.Layers[0].Digest != "sha256:1ayer0ne" || body.Layers[0].Size != 100 {
		t.Errorf("layer[0]: %+v", body.Layers[0])
	}
	if len(body.Manifests) != 0 {
		t.Errorf("manifests should be empty for image kind, got %d", len(body.Manifests))
	}
	// body_base64 must round-trip back to the original bytes.
	dec, err := base64.StdEncoding.DecodeString(body.BodyBase64)
	if err != nil {
		t.Fatalf("body_base64 decode: %v", err)
	}
	if string(dec) != imageManifestBody {
		t.Errorf("body_base64 round-trip mismatch")
	}
	if fake.lastGetID != target {
		t.Errorf("get id: got %q, want %q", fake.lastGetID, target)
	}
}

// TestProxyCacheDetail_Index_HappyPath — manifest-list / image-index
// branches to kind=index with per-platform projection populated and
// layers empty.
func TestProxyCacheDetail_Index_HappyPath(t *testing.T) {
	env, fake := newProxyEnv(t)
	target := "33333333-3333-4333-8333-333333333333"

	fake.getReturn = &proxyv1.CachedManifestDetail{
		Manifest: &proxyv1.CachedManifest{
			Id:           target,
			UpstreamId:   "22222222-2222-4222-8222-222222222222",
			UpstreamName: "dockerhub",
			Image:        "library/nginx",
			Reference:    "stable",
			Digest:       "sha256:idx",
			MediaType:    "application/vnd.oci.image.index.v1+json",
			SizeBytes:    int64(len(indexManifestBody)),
			FetchedAt:    timestamppb.New(time.Now().UTC()),
		},
		Body: []byte(indexManifestBody),
	}
	resp := env.get(t, "/api/v1/proxy/cache/"+target, platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Kind      string `json:"kind"`
		Layers    []any  `json:"layers"`
		Manifests []struct {
			Digest       string `json:"digest"`
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant,omitempty"`
		} `json:"manifests"`
	}
	decodeJSON(t, resp, &body)

	if body.Kind != "index" {
		t.Errorf("kind: got %q, want %q", body.Kind, "index")
	}
	if len(body.Layers) != 0 {
		t.Errorf("layers should be empty for index kind, got %d", len(body.Layers))
	}
	if len(body.Manifests) != 2 {
		t.Fatalf("manifests: got %d, want 2", len(body.Manifests))
	}
	if body.Manifests[0].Architecture != "amd64" || body.Manifests[1].Variant != "v8" {
		t.Errorf("platform projection wrong: %+v", body.Manifests)
	}
}

// TestProxyCacheDetail_NotFound — gRPC NotFound → 404.
func TestProxyCacheDetail_NotFound(t *testing.T) {
	env, fake := newProxyEnv(t)
	fake.getErr = status.Error(codes.NotFound, "cached manifest not found")
	resp := env.get(t, "/api/v1/proxy/cache/55555555-5555-4555-8555-555555555555", platformAdminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// Silence unused-import nags when none of the helpers above happen to
// reference json directly (decodeJSON is in handler_test.go).
var _ = json.Marshal
var _ = base64.StdEncoding
