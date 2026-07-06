// referrers_test.go — tests for the Referrers tab route
// (GET /api/v1/repositories/{org}/{repo}/tags/{tag}/referrers).
//
// These tests stand up their own environment (referrersTestEnv) instead of
// the shared newTestEnv because they need a fake registry-core client wired
// into the handler. They reuse the fakeAuthServer / fakeMetaServer /
// fakeAuditServer from handler_test.go so the tenant/token/repo/tag
// resolution matches every other suite (repo "myorg/myrepo", tag digest
// "sha256:abc123").
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeCoreServer is a configurable in-process CoreService used by the
// referrers tests. Fields are exported so each case can swap in the canned
// response or error it needs.
type fakeCoreServer struct {
	corev1.UnimplementedCoreServiceServer

	// referrers returned by ListReferrers when listErr is nil.
	referrers []*corev1.ReferrerDescriptor
	// filtered is echoed back in the response.
	filtered bool
	// listErr, when non-nil, is returned instead of a response.
	listErr error
	// lastReq captures the last request so tests can assert forwarding of
	// tenant_id / repository / subject_digest.
	lastReq *corev1.ListReferrersRequest

	// blobs maps a digest -> canned blob bytes for GetBlob (FUT-022 chart
	// tab). blobErr, when non-nil, is returned by GetBlob instead (global —
	// fails ALL GetBlob calls, e.g. the core-down test).
	blobs   map[string][]byte
	blobErr error
	// blobErrs maps a digest -> per-digest error so a test can fail one blob
	// (e.g. the content layer) while another (the config) still succeeds.
	// Checked before blobErr / blobs. FUT-022 review coverage.
	blobErrs map[string]error
}

func (s *fakeCoreServer) ListReferrers(_ context.Context, req *corev1.ListReferrersRequest) (*corev1.ListReferrersResponse, error) {
	s.lastReq = req
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &corev1.ListReferrersResponse{Referrers: s.referrers, Filtered: s.filtered}, nil
}

// GetBlob returns the canned bytes seeded in s.blobs keyed by digest, or a
// NotFound status when the digest is unknown. FUT-022 chart-detail tests use
// it to serve the config + content-layer blobs.
func (s *fakeCoreServer) GetBlob(_ context.Context, req *corev1.GetBlobRequest) (*corev1.GetBlobResponse, error) {
	if e, ok := s.blobErrs[req.GetDigest()]; ok {
		return nil, e
	}
	if s.blobErr != nil {
		return nil, s.blobErr
	}
	data, ok := s.blobs[req.GetDigest()]
	if !ok {
		return nil, status.Error(codes.NotFound, "blob not found")
	}
	return &corev1.GetBlobResponse{Data: data, Size: int64(len(data))}, nil
}

// GetBlobStream streams a canned blob for the chart-download route. Reuses the
// same blobs/blobErr/blobErrs fields as GetBlob; sends the bytes in two chunks
// so tests exercise multi-chunk reassembly.
func (s *fakeCoreServer) GetBlobStream(req *corev1.GetBlobRequest, stream corev1.CoreService_GetBlobStreamServer) error {
	if e, ok := s.blobErrs[req.GetDigest()]; ok {
		return e
	}
	if s.blobErr != nil {
		return s.blobErr
	}
	data, ok := s.blobs[req.GetDigest()]
	if !ok {
		return status.Error(codes.NotFound, "blob not found")
	}
	mid := len(data) / 2
	if err := stream.Send(&corev1.GetBlobChunk{Data: data[:mid]}); err != nil {
		return err
	}
	return stream.Send(&corev1.GetBlobChunk{Data: data[mid:]})
}

// referrersTestEnv wraps an httptest.Server with the fake core server so
// tests can configure it per case.
type referrersTestEnv struct {
	srv  *httptest.Server
	core *fakeCoreServer
}

// newReferrersTestEnv builds a handler wired to the standard auth/meta/audit
// fakes plus a fake core client. Pass core=false to leave the core client
// nil (the CORE_GRPC_ADDR-unset case) so the route-gate 404 can be asserted.
func newReferrersTestEnv(t *testing.T, withCore bool) *referrersTestEnv {
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
		nil,
		"",
	)

	env := &referrersTestEnv{}
	if withCore {
		fakeCore := &fakeCoreServer{}
		coreLis := bufconn.Listen(bufSize)
		coreGRPC := grpc.NewServer()
		corev1.RegisterCoreServiceServer(coreGRPC, fakeCore)
		go func() { _ = coreGRPC.Serve(coreLis) }()
		t.Cleanup(coreGRPC.Stop)
		h = h.WithCoreClient(corev1.NewCoreServiceClient(dial(coreLis)))
		env.core = fakeCore
	}
	// When withCore is false the core client is deliberately left nil so the
	// route returns 404 "route disabled".

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	env.srv = srv
	return env
}

func (e *referrersTestEnv) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

const referrersPath = "/api/v1/repositories/myorg/myrepo/tags/v1.0/referrers"

// referrersWire is the parsed shape of the referrers JSON body.
type referrersWire struct {
	Referrers []struct {
		Digest       string            `json:"digest"`
		MediaType    string            `json:"media_type"`
		ArtifactType string            `json:"artifact_type"`
		Size         int64             `json:"size"`
		Annotations  map[string]string `json:"annotations,omitempty"`
	} `json:"referrers"`
	Filtered bool `json:"filtered"`
}

// TestListReferrers_coreUnset_returns404 — the route-gate: when CORE_GRPC_ADDR
// is unset (h.core nil) the route returns 404 "route disabled".
func TestListReferrers_coreUnset_returns404(t *testing.T) {
	env := newReferrersTestEnv(t, false)
	resp := env.get(t, referrersPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when core unset, got %d", resp.StatusCode)
	}
}

// TestListReferrers_happyPath_returnsList asserts the wire shape, that the
// tag's manifest digest is forwarded as the subject, and that the full
// "<org>/<repo>" repository name is passed through.
func TestListReferrers_happyPath_returnsList(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	env.core.referrers = []*corev1.ReferrerDescriptor{
		{
			Digest:       "sha256:ref1",
			MediaType:    "application/vnd.oci.image.manifest.v1+json",
			ArtifactType: "application/vnd.dev.cosign.artifact.sig.v1+json",
			Size:         123,
			Annotations:  map[string]string{"org.opencontainers.image.created": "2026-07-05"},
		},
	}

	resp := env.get(t, referrersPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body referrersWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Referrers) != 1 {
		t.Fatalf("expected 1 referrer, got %d", len(body.Referrers))
	}
	r := body.Referrers[0]
	if r.Digest != "sha256:ref1" {
		t.Errorf("digest: got %q", r.Digest)
	}
	if r.ArtifactType != "application/vnd.dev.cosign.artifact.sig.v1+json" {
		t.Errorf("artifact_type: got %q", r.ArtifactType)
	}
	if r.Size != 123 {
		t.Errorf("size: got %d", r.Size)
	}
	if r.Annotations["org.opencontainers.image.created"] != "2026-07-05" {
		t.Errorf("annotations: got %v", r.Annotations)
	}

	// The subject digest must be the tag's manifest digest from fakeMetaServer,
	// and the repository the full "<org>/<repo>" OCI name.
	if env.core.lastReq.GetSubjectDigest() != "sha256:abc123" {
		t.Errorf("subject_digest: got %q, want sha256:abc123", env.core.lastReq.GetSubjectDigest())
	}
	if env.core.lastReq.GetRepository() != "myorg/myrepo" {
		t.Errorf("repository: got %q, want myorg/myrepo", env.core.lastReq.GetRepository())
	}
	if env.core.lastReq.GetTenantId() != testTenantID {
		t.Errorf("tenant_id: got %q, want %s", env.core.lastReq.GetTenantId(), testTenantID)
	}
}

// TestListReferrers_empty_returnsNonNilArray asserts an empty referrer set
// serialises as `[]`, never `null`.
func TestListReferrers_empty_returnsNonNilArray(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	env.core.referrers = nil // no referrers

	resp := env.get(t, referrersPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Inspect the raw JSON so we can prove the key is `[]` not `null`.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["referrers"]); got != "[]" {
		t.Errorf("expected referrers to serialise as [], got %q", got)
	}
}

// TestListReferrers_invalidTag_returns400 asserts path validation runs before
// any gRPC call.
func TestListReferrers_invalidTag_returns400(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/bad$tag/referrers", adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid tag, got %d", resp.StatusCode)
	}
}

// TestListReferrers_unknownRepo_returns404 — the metadata fake doesn't know
// this repo, so findRepo fails and the handler returns 404.
func TestListReferrers_unknownRepo_returns404(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	resp := env.get(t, "/api/v1/repositories/myorg/nosuchthing/tags/v1.0/referrers", adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown repo, got %d", resp.StatusCode)
	}
}

// TestListReferrers_coreNotFound_returns404 — registry-core returns NotFound
// (subject unknown); the handler maps it to 404.
func TestListReferrers_coreNotFound_returns404(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	env.core.listErr = status.Error(codes.NotFound, "no such manifest")
	resp := env.get(t, referrersPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
