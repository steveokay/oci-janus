// signature_test.go — tests for FE-API-003 (default GET) + FE-API-025
// (`?verify=true` opt-in cryptographic verification) +
// FE-API-026 (POST …/sign).
//
// These tests use their own test environment (signerTestEnv) instead of the
// shared newTestEnv because they need a fake signer + a fake publisher
// attached to the handler. handler_test.go's newTestEnv is left untouched so
// the broader test suite keeps its existing wiring.
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeSignerServer is a configurable in-process SignerService used by the
// signature + sign_manifest tests. The fields are exported so each test can
// swap in the canned response or behaviour it needs.
type fakeSignerServer struct {
	signerv1.UnimplementedSignerServiceServer

	// signatures returned by ListSignatures. nil yields a single canned entry.
	signatures []*signerv1.Signature

	// signaturesByDigest, when non-nil, makes ListSignatures answer per
	// manifest_digest: a hit returns those signatures, a miss returns
	// NotFound (i.e. an unsigned manifest). Lets the coverage rollup tests
	// mark specific digests signed vs unsigned. Takes precedence over
	// `signatures` when set.
	signaturesByDigest map[string][]*signerv1.Signature

	// listCalls counts ListSignatures invocations so the coverage test can
	// assert per-digest dedupe (two tags → one manifest → one call).
	listCalls int

	// listErr is returned from ListSignatures when non-nil; takes precedence
	// over signatures.
	listErr error

	// verifyFunc lets each test customise VerifyManifest per signer_id —
	// used by FE-API-025 tests to mark one signature as failing or sleeping.
	// When nil, every verify returns verified=true.
	verifyFunc func(ctx context.Context, req *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error)

	// signFunc overrides SignManifest. When nil the server returns a canned
	// signature record echoing the request fields. Errors from this func
	// are returned verbatim so tests can simulate AlreadyExists etc.
	signFunc func(ctx context.Context, req *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error)

	// signCalls tracks the requests SignManifest has received so tests can
	// assert tenant_id / digest / signer_id forwarding.
	signCalls []*signerv1.SignManifestRequest
	mu        sync.Mutex
}

func (s *fakeSignerServer) ListSignatures(_ context.Context, req *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
	s.mu.Lock()
	s.listCalls++
	s.mu.Unlock()

	if s.listErr != nil {
		return nil, s.listErr
	}
	// Per-digest table wins when configured (coverage rollup tests).
	if s.signaturesByDigest != nil {
		sigs, ok := s.signaturesByDigest[req.GetManifestDigest()]
		if !ok || len(sigs) == 0 {
			return nil, status.Error(codes.NotFound, "no signatures for digest")
		}
		return &signerv1.ListSignaturesResponse{Signatures: sigs}, nil
	}
	if s.signatures == nil {
		return &signerv1.ListSignaturesResponse{
			Signatures: []*signerv1.Signature{
				{
					SignerId:        "signer-A",
					KeyId:           "key-A",
					SignatureDigest: "sha256:sigA",
					ManifestDigest:  "sha256:abc123",
					SignedAt:        timestamppb.Now(),
				},
			},
		}, nil
	}
	return &signerv1.ListSignaturesResponse{Signatures: s.signatures}, nil
}

func (s *fakeSignerServer) VerifyManifest(ctx context.Context, req *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error) {
	if s.verifyFunc != nil {
		return s.verifyFunc(ctx, req)
	}
	// Default: everything verifies cleanly.
	return &signerv1.VerifyManifestResponse{Verified: true}, nil
}

func (s *fakeSignerServer) SignManifest(ctx context.Context, req *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error) {
	s.mu.Lock()
	s.signCalls = append(s.signCalls, req)
	s.mu.Unlock()

	if s.signFunc != nil {
		return s.signFunc(ctx, req)
	}
	// Default: echo back a freshly minted signature record. KeyID derived from
	// signer_id so tests can verify wire mapping without a separate lookup.
	return &signerv1.SignManifestResponse{
		Signature: &signerv1.Signature{
			SignerId:        req.GetSignerId(),
			KeyId:           "key-for-" + req.GetSignerId(),
			SignatureDigest: "sha256:fresh-sig",
			ManifestDigest:  req.GetManifestDigest(),
			SignedAt:        timestamppb.Now(),
		},
	}, nil
}

// fakePublisher captures published events for assertion. Satisfies
// handler.EventPublisher.
type fakePublisher struct {
	mu         sync.Mutex
	calls      []publishCall
	publishErr error // optional error to return from Publish.
	count      int64 // atomic count, useful for "was publish called?" gating.
}

type publishCall struct {
	routingKey string
	event      events.Event
}

func (p *fakePublisher) Publish(_ context.Context, routingKey string, event events.Event) error {
	atomic.AddInt64(&p.count, 1)
	if p.publishErr != nil {
		return p.publishErr
	}
	p.mu.Lock()
	p.calls = append(p.calls, publishCall{routingKey: routingKey, event: event})
	p.mu.Unlock()
	return nil
}

// signerTestEnv wraps an httptest.Server with the fakes the signing tests
// need. Mirrors testEnv from handler_test.go but adds the signer + publisher
// so tests can configure them per case.
type signerTestEnv struct {
	srv    *httptest.Server
	signer *fakeSignerServer
	pub    *fakePublisher
}

// newSignerTestEnv builds a handler wired to the standard auth/meta/audit
// fakes plus a fake signer + capturing publisher. The signer/publisher are
// returned so individual tests can configure them.
func newSignerTestEnv(t *testing.T) *signerTestEnv {
	t.Helper()

	// Auth bufconn — reuse fakeAuthServer from handler_test.go.
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	// Metadata bufconn — reuse fakeMetaServer.
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	// Audit bufconn.
	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	// Signer bufconn — case under test.
	fakeSigner := &fakeSignerServer{}
	signerLis := bufconn.Listen(bufSize)
	signerGRPC := grpc.NewServer()
	signerv1.RegisterSignerServiceServer(signerGRPC, fakeSigner)
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

	authConn := dial(authLis)
	metaConn := dial(metaLis)
	auditConn := dial(auditLis)
	signerConn := dial(signerLis)

	pub := &fakePublisher{}

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, // publisher swapped in via WithPublisher below — *publisher.Publisher would need a live broker.
		"",  // platformAdminTenantID
	)
	h = h.WithSignerClient(signerv1.NewSignerServiceClient(signerConn))
	h = h.WithPublisher(pub)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &signerTestEnv{srv: srv, signer: fakeSigner, pub: pub}
}

// signerlessTestEnv builds a handler with the signer client deliberately
// unset, used to assert the "route disabled" gate for the sign route.
func newSignerlessTestEnv(t *testing.T) *httptest.Server {
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

	authConn := dial(authLis)
	metaConn := dial(metaLis)
	auditConn := dial(auditLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil,
		"",
	)
	// Signer client deliberately left nil so the route returns 404.

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// Helpers — request helpers mirroring testEnv.* on signerTestEnv.
// ---------------------------------------------------------------------------

func (e *signerTestEnv) get(t *testing.T, path, token string) *http.Response {
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

func (e *signerTestEnv) post(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// FE-API-003 baseline + FE-API-025 verify tests
// ---------------------------------------------------------------------------

// signatureWire is the parsed shape of the GET /signature JSON. Defined here
// (rather than reusing handler.SignatureResponse) because the verified +
// failure_reason fields are unexported in the handler package.
type signatureWire struct {
	ManifestDigest string `json:"manifest_digest"`
	Signed         bool   `json:"signed"`
	Signatures     []struct {
		SignerID        string `json:"signer_id"`
		KeyID           string `json:"key_id"`
		SignatureDigest string `json:"signature_digest"`
		SignedAt        string `json:"signed_at"`
		Verified        *bool  `json:"verified,omitempty"`
		FailureReason   string `json:"failure_reason,omitempty"`
	} `json:"signatures"`
}

// TestGetSignature_default_omitsVerifiedField confirms the FE-API-003 wire
// shape is preserved when the caller does NOT pass ?verify=true.
func TestGetSignature_default_omitsVerifiedField(t *testing.T) {
	env := newSignerTestEnv(t)
	env.signer.signatures = []*signerv1.Signature{
		{SignerId: "alice", KeyId: "k1", SignatureDigest: "sha256:1", SignedAt: timestamppb.Now()},
	}

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/signature", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Read raw body to confirm "verified" key is NOT present in the JSON.
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sigs, _ := raw["signatures"].([]any)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	sigMap, _ := sigs[0].(map[string]any)
	if _, ok := sigMap["verified"]; ok {
		t.Errorf("verified field should be omitted on default path, got map=%v", sigMap)
	}
}

// TestGetSignature_verifyTrue_allOK marks both signatures as verified=true.
func TestGetSignature_verifyTrue_allOK(t *testing.T) {
	env := newSignerTestEnv(t)
	env.signer.signatures = []*signerv1.Signature{
		{SignerId: "alice", KeyId: "k1", SignatureDigest: "sha256:1", SignedAt: timestamppb.Now()},
		{SignerId: "bob", KeyId: "k2", SignatureDigest: "sha256:2", SignedAt: timestamppb.Now()},
	}
	// Default verifyFunc returns verified=true for any signer.

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/signature?verify=true", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body signatureWire
	decodeJSON(t, resp, &body)
	if len(body.Signatures) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(body.Signatures))
	}
	for i, s := range body.Signatures {
		if s.Verified == nil || !*s.Verified {
			t.Errorf("signature[%d]: expected verified=true, got %+v", i, s)
		}
		if s.FailureReason != "" {
			t.Errorf("signature[%d]: expected empty failure_reason, got %q", i, s.FailureReason)
		}
	}
}

// TestGetSignature_verifyTrue_oneFails — one signature reports verified=false
// with a failure reason; the other passes.
func TestGetSignature_verifyTrue_oneFails(t *testing.T) {
	env := newSignerTestEnv(t)
	env.signer.signatures = []*signerv1.Signature{
		{SignerId: "alice", KeyId: "k1", SignatureDigest: "sha256:1", SignedAt: timestamppb.Now()},
		{SignerId: "bad", KeyId: "k2", SignatureDigest: "sha256:2", SignedAt: timestamppb.Now()},
	}
	env.signer.verifyFunc = func(_ context.Context, req *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error) {
		if req.GetSignerId() == "bad" {
			return &signerv1.VerifyManifestResponse{
				Verified:      false,
				FailureReason: "x509: certificate signed by unknown authority",
			}, nil
		}
		return &signerv1.VerifyManifestResponse{Verified: true}, nil
	}

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/signature?verify=true", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body signatureWire
	decodeJSON(t, resp, &body)
	if len(body.Signatures) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(body.Signatures))
	}

	// Map by signer_id since fan-out is parallel and slice order is preserved
	// but we want to assert per-signer state explicitly.
	byID := make(map[string]int)
	for i, s := range body.Signatures {
		byID[s.SignerID] = i
	}
	good := body.Signatures[byID["alice"]]
	bad := body.Signatures[byID["bad"]]

	if good.Verified == nil || !*good.Verified {
		t.Errorf("alice: expected verified=true, got %+v", good)
	}
	if bad.Verified == nil || *bad.Verified {
		t.Errorf("bad: expected verified=false, got %+v", bad)
	}
	if bad.FailureReason == "" || !strings.Contains(bad.FailureReason, "x509") {
		t.Errorf("bad: expected failure_reason to mention x509, got %q", bad.FailureReason)
	}
}

// TestGetSignature_verifyTrue_timeout — the verify call exceeds the
// per-signature deadline; record reports verified=false with a synthetic
// "verification timed out" reason.
func TestGetSignature_verifyTrue_timeout(t *testing.T) {
	env := newSignerTestEnv(t)
	env.signer.signatures = []*signerv1.Signature{
		{SignerId: "slow", KeyId: "k1", SignatureDigest: "sha256:1", SignedAt: timestamppb.Now()},
	}
	env.signer.verifyFunc = func(ctx context.Context, _ *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error) {
		// Sleep until the per-signature deadline fires (5s in production;
		// we wait on ctx so the test still finishes quickly).
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, "deadline")
		case <-time.After(30 * time.Second):
			return &signerv1.VerifyManifestResponse{Verified: true}, nil
		}
	}

	// Use a shorter overall HTTP-side timeout so the test caps at ~6s rather
	// than the full 5s per-signature handler timeout — but the handler still
	// honours its own 5s deadline first, so we expect a response well inside
	// 10s.
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet,
		env.srv.URL+"/api/v1/repositories/myorg/myrepo/tags/v1.0/signature?verify=true", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET signature: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body signatureWire
	decodeJSON(t, resp, &body)
	if len(body.Signatures) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(body.Signatures))
	}
	s := body.Signatures[0]
	if s.Verified == nil || *s.Verified {
		t.Errorf("expected verified=false, got %+v", s)
	}
	if s.FailureReason != "verification timed out" {
		t.Errorf("expected failure_reason='verification timed out', got %q", s.FailureReason)
	}
}

// ---------------------------------------------------------------------------
// FE-API-026 — POST /sign
// ---------------------------------------------------------------------------

// TestSignManifest_happyPath_returns201 asserts a fresh sign returns 201 with
// the new record, the signer was called with the right digest + signer_id,
// and an image.signed event was published.
func TestSignManifest_happyPath_returns201(t *testing.T) {
	env := newSignerTestEnv(t)

	body := `{"signer_id":"alice-key-v1"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var record struct {
		SignerID        string `json:"signer_id"`
		KeyID           string `json:"key_id"`
		SignatureDigest string `json:"signature_digest"`
	}
	decodeJSON(t, resp, &record)
	if record.SignerID != "alice-key-v1" {
		t.Errorf("expected signer_id=alice-key-v1, got %q", record.SignerID)
	}
	if record.SignatureDigest == "" {
		t.Errorf("expected non-empty signature_digest")
	}

	// Signer must have seen exactly one SignManifest call with the right
	// digest (the canned digest from fakeMetaServer.GetTag).
	env.signer.mu.Lock()
	defer env.signer.mu.Unlock()
	if len(env.signer.signCalls) != 1 {
		t.Fatalf("expected 1 SignManifest call, got %d", len(env.signer.signCalls))
	}
	call := env.signer.signCalls[0]
	if call.GetManifestDigest() != "sha256:abc123" {
		t.Errorf("expected digest=sha256:abc123, got %q", call.GetManifestDigest())
	}
	if call.GetSignerId() != "alice-key-v1" {
		t.Errorf("expected signer_id=alice-key-v1, got %q", call.GetSignerId())
	}

	// And the image.signed event must have been published.
	if atomic.LoadInt64(&env.pub.count) != 1 {
		t.Errorf("expected 1 publish, got %d", env.pub.count)
	}
	env.pub.mu.Lock()
	defer env.pub.mu.Unlock()
	if len(env.pub.calls) != 1 {
		t.Fatalf("expected 1 captured publish, got %d", len(env.pub.calls))
	}
	pc := env.pub.calls[0]
	if pc.routingKey != events.RoutingImageSigned {
		t.Errorf("expected routing key %q, got %q", events.RoutingImageSigned, pc.routingKey)
	}
}

// TestSignManifest_readerToken_returns403 — a reader user must not be able
// to sign.
func TestSignManifest_readerToken_returns403(t *testing.T) {
	env := newSignerTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", readerToken, `{"signer_id":"k1"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestSignManifest_emptySignerID_returns400.
func TestSignManifest_emptySignerID_returns400(t *testing.T) {
	env := newSignerTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, `{"signer_id":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestSignManifest_unknownRepo_returns404 — the URL points at a repo the
// metadata fake doesn't know about. Note: the auth fake only grants
// permissions for "myorg" so a different org would 403; we use the right
// org but a missing repo name so the gate falls to the metadata lookup.
func TestSignManifest_unknownRepo_returns404(t *testing.T) {
	env := newSignerTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/nosuchthing/tags/v1.0/sign", adminToken, `{"signer_id":"k1"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestSignManifest_signerDisabled_returns404 — when the signer client is
// nil the route is gated off entirely.
func TestSignManifest_signerDisabled_returns404(t *testing.T) {
	srv := newSignerlessTestEnv(t)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/repositories/myorg/myrepo/tags/v1.0/sign",
		strings.NewReader(`{"signer_id":"k1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestSignManifest_alreadySigned_returns409 — signer rejects re-sign with
// AlreadyExists; handler maps it to 409.
func TestSignManifest_alreadySigned_returns409(t *testing.T) {
	env := newSignerTestEnv(t)
	env.signer.signFunc = func(_ context.Context, _ *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error) {
		return nil, status.Error(codes.AlreadyExists, "already signed")
	}
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, `{"signer_id":"k1"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}
