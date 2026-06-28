// FUT-018 — digest-keyed scan + signature route tests.
//
// Coverage matrix:
//   - h.scanner / h.signer nil → 404 on the matching routes
//   - readerToken POST → 403 (writer gate)
//   - invalid digest → 400 (BFF regex guard)
//   - GET scan with no recorded scan → 404 "no scan recorded"
//   - GET signatures with zero sigs → 200 + Signed:false
//   - POST scan → 202 + scan_id + event publish
//   - POST sign → 200 + signature shape
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeMetaServerForDigest extends fakeMetaServer with a configurable
// GetScanResult. The base server in handler_test.go doesn't currently
// stub GetScanResult, so we register this dedicated implementation on
// its own bufconn for the FUT-018 tests.
type fakeMetaServerForDigest struct {
	metadatav1.UnimplementedMetadataServiceServer
	mu             sync.Mutex
	scanResult     *metadatav1.ScanResult
	scanResultErr  error
	lastScanLookup *metadatav1.GetScanResultRequest
}

func (m *fakeMetaServerForDigest) GetScanResult(_ context.Context, req *metadatav1.GetScanResultRequest) (*metadatav1.ScanResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastScanLookup = req
	if m.scanResultErr != nil {
		return nil, m.scanResultErr
	}
	if m.scanResult != nil {
		return m.scanResult, nil
	}
	return nil, status.Error(codes.NotFound, "no scan recorded")
}

// fakeDigestSigner stubs the signer endpoints FUT-018 uses.
type fakeDigestSigner struct {
	signerv1.UnimplementedSignerServiceServer
	mu       sync.Mutex
	list     *signerv1.ListSignaturesResponse
	listErr  error
	sign     *signerv1.SignManifestResponse
	signErr  error
	lastSign *signerv1.SignManifestRequest
}

func (s *fakeDigestSigner) ListSignatures(_ context.Context, _ *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.list != nil {
		return s.list, nil
	}
	return &signerv1.ListSignaturesResponse{}, nil
}

func (s *fakeDigestSigner) SignManifest(_ context.Context, req *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSign = req
	if s.signErr != nil {
		return nil, s.signErr
	}
	if s.sign != nil {
		return s.sign, nil
	}
	return &signerv1.SignManifestResponse{
		Signature: &signerv1.Signature{
			SignerId:        "test-signer",
			ManifestDigest:  req.GetManifestDigest(),
			SignatureDigest: "sig:abc",
			KeyId:           "workspace-default",
			SignedAt:        timestamppb.Now(),
		},
	}, nil
}

// fakeScannerStub is the bare minimum scanner fake — FUT-018 only needs
// the client to exist (non-nil) for the route-disabled gate to pass.
// The actual scan-trigger path goes through the publisher, not the
// scanner client.
type fakeScannerStub struct {
	scannerv1.UnimplementedScannerServiceServer
}

// recordingPublisher captures Publish() calls instead of hitting RabbitMQ.
type recordingPublisher struct {
	mu       sync.Mutex
	lastKey  string
	lastEvt  events.Event
	count    int
	failNext bool
}

func (p *recordingPublisher) Publish(_ context.Context, routingKey string, evt events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext {
		p.failNext = false
		return status.Error(codes.Internal, "broker oops")
	}
	p.lastKey = routingKey
	p.lastEvt = evt
	p.count++
	return nil
}

// newDigestKeyedEnv stands up the bufconn stack with all four backing
// services wired so the FUT-018 routes can be exercised end-to-end.
func newDigestKeyedEnv(t *testing.T) (*testEnv, *fakeMetaServerForDigest, *fakeDigestSigner, *recordingPublisher) {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	fakeMeta := &fakeMetaServerForDigest{}
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, fakeMeta)
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	scannerLis := bufconn.Listen(bufSize)
	scannerGRPC := grpc.NewServer()
	scannerv1.RegisterScannerServiceServer(scannerGRPC, &fakeScannerStub{})
	healthpb.RegisterHealthServer(scannerGRPC, &fakeHealthServer{})
	go func() { _ = scannerGRPC.Serve(scannerLis) }()
	t.Cleanup(scannerGRPC.Stop)

	fakeSigner := &fakeDigestSigner{}
	signerLis := bufconn.Listen(bufSize)
	signerGRPC := grpc.NewServer()
	signerv1.RegisterSignerServiceServer(signerGRPC, fakeSigner)
	healthpb.RegisterHealthServer(signerGRPC, &fakeHealthServer{})
	go func() { _ = signerGRPC.Serve(signerLis) }()
	t.Cleanup(signerGRPC.Stop)

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

	pub := &recordingPublisher{}

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil, // no real *publisher.Publisher; injected next line via WithPublisher
		"",
		healthpb.NewHealthClient(dial(authLis)),
	).
		WithScannerClient(scannerv1.NewScannerServiceClient(dial(scannerLis))).
		WithSignerClient(signerv1.NewSignerServiceClient(dial(signerLis))).
		WithPublisher(pub)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeMeta, fakeSigner, pub
}

const validDigest = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

// ── Route-disabled cases ───────────────────────────────────────────

func TestDigestKeyed_ScannerNil_postScan404(t *testing.T) {
	env := newTestEnv(t) // no scanner wired
	resp := env.post(t, "/api/v1/scan-by-digest/"+validDigest, adminToken, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDigestKeyed_SignerNil_routes404(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{
		"/api/v1/signatures-by-digest/" + validDigest,
		"/api/v1/sign-by-digest/" + validDigest,
	} {
		var resp *http.Response
		if strings.HasPrefix(path, "/api/v1/sign-by-digest") {
			resp = env.post(t, path, adminToken, "")
		} else {
			resp = env.get(t, path, adminToken)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

// ── Validation ─────────────────────────────────────────────────────

func TestDigestKeyed_InvalidDigest_returns400(t *testing.T) {
	env, _, _, _ := newDigestKeyedEnv(t)
	// Wrong prefix, too short, wrong charset
	bad := []string{"sha512:abc", "sha256:short", "sha256:ZZZ" + strings.Repeat("0", 61)}
	for _, d := range bad {
		resp := env.get(t, "/api/v1/scan-by-digest/"+d, adminToken)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("digest %q: expected 400, got %d", d, resp.StatusCode)
		}
	}
}

// ── GET scan ───────────────────────────────────────────────────────

func TestDigestKeyed_GetScan_noRecorded_returns404(t *testing.T) {
	env, _, _, _ := newDigestKeyedEnv(t)
	resp := env.get(t, "/api/v1/scan-by-digest/"+validDigest, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDigestKeyed_GetScan_returnsResult(t *testing.T) {
	env, fakeMeta, _, _ := newDigestKeyedEnv(t)
	fakeMeta.scanResult = &metadatav1.ScanResult{
		ScanId:         "scan-1",
		ManifestDigest: validDigest,
		Status:         "complete",
		ScannerName:    "trivy",
		ScannerVersion: "0.50",
		SeverityCounts: map[string]int32{"high": 2, "medium": 5},
	}
	resp := env.get(t, "/api/v1/scan-by-digest/"+validDigest, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fakeMeta.lastScanLookup.GetRepoId() != "" {
		t.Errorf("BFF should pass empty repo_id to metadata, got %q", fakeMeta.lastScanLookup.GetRepoId())
	}
}

// ── POST scan ──────────────────────────────────────────────────────

func TestDigestKeyed_PostScan_readerToken_returns403(t *testing.T) {
	env, _, _, _ := newDigestKeyedEnv(t)
	resp := env.post(t, "/api/v1/scan-by-digest/"+validDigest, readerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDigestKeyed_PostScan_publishesEvent(t *testing.T) {
	env, _, _, pub := newDigestKeyedEnv(t)
	resp := env.post(t, "/api/v1/scan-by-digest/"+validDigest, adminToken, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if pub.count != 1 {
		t.Fatalf("publish count: %d, want 1", pub.count)
	}
	if pub.lastKey != events.RoutingScanQueued {
		t.Errorf("routing key: got %q, want %q", pub.lastKey, events.RoutingScanQueued)
	}
}

// ── GET signatures ─────────────────────────────────────────────────

func TestDigestKeyed_GetSignatures_zeroSigs_returnsUnsigned(t *testing.T) {
	env, _, _, _ := newDigestKeyedEnv(t)
	resp := env.get(t, "/api/v1/signatures-by-digest/"+validDigest, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Signed     bool `json:"signed"`
		Signatures []struct {
			SignerID string `json:"signer_id"`
		} `json:"signatures"`
	}
	decodeJSON(t, resp, &body)
	if body.Signed || len(body.Signatures) != 0 {
		t.Errorf("expected unsigned shape, got %+v", body)
	}
}

func TestDigestKeyed_GetSignatures_signed(t *testing.T) {
	env, _, fakeSigner, _ := newDigestKeyedEnv(t)
	fakeSigner.list = &signerv1.ListSignaturesResponse{
		Signatures: []*signerv1.Signature{
			{
				SignerId:        "test-signer",
				ManifestDigest:  validDigest,
				SignatureDigest: "sig:1",
				KeyId:           "key-a",
				SignedAt:        timestamppb.Now(),
			},
		},
	}
	resp := env.get(t, "/api/v1/signatures-by-digest/"+validDigest, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Signed     bool `json:"signed"`
		Signatures []struct {
			SignerID string `json:"signer_id"`
			KeyID    string `json:"key_id"`
		} `json:"signatures"`
	}
	decodeJSON(t, resp, &body)
	if !body.Signed || len(body.Signatures) != 1 || body.Signatures[0].KeyID != "key-a" {
		t.Errorf("signed shape: %+v", body)
	}
}

// ── POST sign ──────────────────────────────────────────────────────

func TestDigestKeyed_PostSign_readerToken_returns403(t *testing.T) {
	env, _, _, _ := newDigestKeyedEnv(t)
	resp := env.post(t, "/api/v1/sign-by-digest/"+validDigest, readerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDigestKeyed_PostSign_happy(t *testing.T) {
	env, _, fakeSigner, _ := newDigestKeyedEnv(t)
	resp := env.post(t, "/api/v1/sign-by-digest/"+validDigest, adminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fakeSigner.lastSign == nil {
		t.Fatal("SignManifest never called")
	}
	if fakeSigner.lastSign.GetManifestDigest() != validDigest {
		t.Errorf("digest: got %q, want %q", fakeSigner.lastSign.GetManifestDigest(), validDigest)
	}
}
