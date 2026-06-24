// FUT-017 — proxy_cache_policies_test.go covers the six new
// /api/v1/proxy/upstreams/{name}/{scan,sign}-policy + list routes.
// Mirrors the bufconn fake-server pattern proxy_cache_test.go uses.
//
// Coverage matrix:
//   - h.scanner / h.signer nil → 404 (route-disabled)
//   - readerToken → 403 (workspace-admin gate)
//   - invalid upstream name → 400
//   - invalid severity_threshold → 400
//   - auto_sign=true with empty key_id → 400 (BFF-only safety guard)
//   - happy GET / PUT / List for both scan + sign
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ─── Fake scanner (extends with FUT-017 RPCs) ──────────────────────

type fakePolicyScannerServer struct {
	scannerv1.UnimplementedScannerServiceServer

	mu sync.Mutex

	scanGet    map[string]*scannerv1.ProxyCacheScanPolicy // keyed by upstream_name
	scanGetErr error

	scanSetReturn *scannerv1.ProxyCacheScanPolicy
	scanSetErr    error
	lastSetReq    *scannerv1.SetProxyCacheScanPolicyRequest

	scanList []*scannerv1.ProxyCacheScanPolicy
}

func newFakePolicyScanner() *fakePolicyScannerServer {
	return &fakePolicyScannerServer{
		scanGet: make(map[string]*scannerv1.ProxyCacheScanPolicy),
	}
}

func (s *fakePolicyScannerServer) GetProxyCacheScanPolicy(_ context.Context, req *scannerv1.GetProxyCacheScanPolicyRequest) (*scannerv1.ProxyCacheScanPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanGetErr != nil {
		return nil, s.scanGetErr
	}
	if p, ok := s.scanGet[req.GetUpstreamName()]; ok {
		return p, nil
	}
	// Mirror the real server: default-shaped policy on miss.
	return &scannerv1.ProxyCacheScanPolicy{
		TenantId:          req.GetTenantId(),
		UpstreamName:      req.GetUpstreamName(),
		AutoScan:          false,
		SeverityThreshold: "",
	}, nil
}

func (s *fakePolicyScannerServer) SetProxyCacheScanPolicy(_ context.Context, req *scannerv1.SetProxyCacheScanPolicyRequest) (*scannerv1.ProxyCacheScanPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSetReq = req
	if s.scanSetErr != nil {
		return nil, s.scanSetErr
	}
	if s.scanSetReturn != nil {
		return s.scanSetReturn, nil
	}
	return &scannerv1.ProxyCacheScanPolicy{
		TenantId:          req.GetTenantId(),
		UpstreamName:      req.GetUpstreamName(),
		AutoScan:          req.GetAutoScan(),
		SeverityThreshold: req.GetSeverityThreshold(),
		UpdatedAt:         timestamppb.Now(),
		UpdatedBy:         req.GetUpdatedBy(),
	}, nil
}

func (s *fakePolicyScannerServer) ListProxyCacheScanPolicies(req *scannerv1.ListProxyCacheScanPoliciesRequest, stream scannerv1.ScannerService_ListProxyCacheScanPoliciesServer) error {
	s.mu.Lock()
	rows := append([]*scannerv1.ProxyCacheScanPolicy(nil), s.scanList...)
	s.mu.Unlock()
	for _, p := range rows {
		if err := stream.Send(p); err != nil {
			return err
		}
	}
	_ = req
	return nil
}

// ─── Fake signer (extends with FUT-017 RPCs) ───────────────────────

type fakePolicySignerServer struct {
	signerv1.UnimplementedSignerServiceServer

	mu sync.Mutex

	signGet    map[string]*signerv1.ProxyCacheSignPolicy
	signGetErr error

	signSetReturn *signerv1.ProxyCacheSignPolicy
	signSetErr    error
	lastSetReq    *signerv1.SetProxyCacheSignPolicyRequest

	signList []*signerv1.ProxyCacheSignPolicy
}

func newFakePolicySigner() *fakePolicySignerServer {
	return &fakePolicySignerServer{
		signGet: make(map[string]*signerv1.ProxyCacheSignPolicy),
	}
}

func (s *fakePolicySignerServer) GetProxyCacheSignPolicy(_ context.Context, req *signerv1.GetProxyCacheSignPolicyRequest) (*signerv1.ProxyCacheSignPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signGetErr != nil {
		return nil, s.signGetErr
	}
	if p, ok := s.signGet[req.GetUpstreamName()]; ok {
		return p, nil
	}
	return &signerv1.ProxyCacheSignPolicy{
		TenantId:     req.GetTenantId(),
		UpstreamName: req.GetUpstreamName(),
		AutoSign:     false,
		KeyId:        "",
	}, nil
}

func (s *fakePolicySignerServer) SetProxyCacheSignPolicy(_ context.Context, req *signerv1.SetProxyCacheSignPolicyRequest) (*signerv1.ProxyCacheSignPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSetReq = req
	if s.signSetErr != nil {
		return nil, s.signSetErr
	}
	if s.signSetReturn != nil {
		return s.signSetReturn, nil
	}
	return &signerv1.ProxyCacheSignPolicy{
		TenantId:     req.GetTenantId(),
		UpstreamName: req.GetUpstreamName(),
		AutoSign:     req.GetAutoSign(),
		KeyId:        req.GetKeyId(),
		UpdatedAt:    timestamppb.Now(),
	}, nil
}

func (s *fakePolicySignerServer) ListProxyCacheSignPolicies(req *signerv1.ListProxyCacheSignPoliciesRequest, stream signerv1.SignerService_ListProxyCacheSignPoliciesServer) error {
	s.mu.Lock()
	rows := append([]*signerv1.ProxyCacheSignPolicy(nil), s.signList...)
	s.mu.Unlock()
	for _, p := range rows {
		if err := stream.Send(p); err != nil {
			return err
		}
	}
	_ = req
	return nil
}

// ─── Env helper ────────────────────────────────────────────────────

func newCachePoliciesEnv(t *testing.T) (*testEnv, *fakePolicyScannerServer, *fakePolicySignerServer) {
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

	fakeScanner := newFakePolicyScanner()
	scannerLis := bufconn.Listen(bufSize)
	scannerGRPC := grpc.NewServer()
	scannerv1.RegisterScannerServiceServer(scannerGRPC, fakeScanner)
	healthpb.RegisterHealthServer(scannerGRPC, &fakeHealthServer{})
	go func() { _ = scannerGRPC.Serve(scannerLis) }()
	t.Cleanup(scannerGRPC.Stop)

	fakeSigner := newFakePolicySigner()
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

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil,
		"",
		healthpb.NewHealthClient(dial(authLis)),
	).
		WithScannerClient(scannerv1.NewScannerServiceClient(dial(scannerLis))).
		WithSignerClient(signerv1.NewSignerServiceClient(dial(signerLis)))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeScanner, fakeSigner
}

// ─── Tests ─────────────────────────────────────────────────────────

// Scan-policy routes 404 when h.scanner is nil. Sign-policy routes 404
// when h.signer is nil. Independent — wiring one shouldn't enable the
// other.
func TestProxyCachePolicies_Disabled_returns404(t *testing.T) {
	env := newTestEnv(t) // no scanner or signer wired
	for _, path := range []string{
		"/api/v1/proxy/upstreams/dockerhub/scan-policy",
		"/api/v1/proxy/upstreams/dockerhub/sign-policy",
		"/api/v1/proxy/cache/scan-policies",
		"/api/v1/proxy/cache/sign-policies",
	} {
		resp := env.get(t, path, adminToken)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestProxyCachePolicies_NonAdmin_returns403(t *testing.T) {
	env, _, _ := newCachePoliciesEnv(t)
	for _, path := range []string{
		"/api/v1/proxy/upstreams/dockerhub/scan-policy",
		"/api/v1/proxy/upstreams/dockerhub/sign-policy",
		"/api/v1/proxy/cache/scan-policies",
		"/api/v1/proxy/cache/sign-policies",
	} {
		resp := env.get(t, path, readerToken)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: expected 403 for reader, got %d", path, resp.StatusCode)
		}
	}
}

func TestProxyCachePolicies_InvalidUpstreamName_returns400(t *testing.T) {
	env, _, _ := newCachePoliciesEnv(t)
	// Capital letters, slashes, and over-long names all rejected.
	for _, bad := range []string{"UPPER", "with/slash", "with space", ""} {
		resp := env.get(t, "/api/v1/proxy/upstreams/"+bad+"/scan-policy", adminToken)
		// Empty name routes don't match the pattern; net/http returns
		// 404 for the no-match case. The other invalid forms 400.
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("upstream %q: expected 400/404, got %d", bad, resp.StatusCode)
		}
	}
}

func TestScanPolicy_Get_returnsDefaultOnMiss(t *testing.T) {
	env, _, _ := newCachePoliciesEnv(t)
	resp := env.get(t, "/api/v1/proxy/upstreams/dockerhub/scan-policy", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		UpstreamName      string `json:"upstream_name"`
		AutoScan          bool   `json:"auto_scan"`
		SeverityThreshold string `json:"severity_threshold"`
	}
	decodeJSON(t, resp, &body)
	if body.UpstreamName != "dockerhub" || body.AutoScan != false || body.SeverityThreshold != "" {
		t.Errorf("default policy: %+v", body)
	}
}

func TestScanPolicy_Put_propagates(t *testing.T) {
	env, fakeScanner, _ := newCachePoliciesEnv(t)
	body := `{"auto_scan":true,"severity_threshold":"high"}`
	resp := env.put(t, "/api/v1/proxy/upstreams/dockerhub/scan-policy", adminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fakeScanner.lastSetReq == nil {
		t.Fatal("upstream Set call never happened")
	}
	if !fakeScanner.lastSetReq.GetAutoScan() || fakeScanner.lastSetReq.GetSeverityThreshold() != "high" {
		t.Errorf("Set payload didn't propagate: %+v", fakeScanner.lastSetReq)
	}
}

func TestScanPolicy_Put_invalidSeverity_returns400(t *testing.T) {
	env, _, _ := newCachePoliciesEnv(t)
	resp := env.put(t, "/api/v1/proxy/upstreams/dockerhub/scan-policy", adminToken, `{"auto_scan":true,"severity_threshold":"banana"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSignPolicy_Put_autoSignWithoutKey_returns400(t *testing.T) {
	env, _, _ := newCachePoliciesEnv(t)
	resp := env.put(t, "/api/v1/proxy/upstreams/dockerhub/sign-policy", adminToken, `{"auto_sign":true,"key_id":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when auto_sign=true with empty key_id, got %d", resp.StatusCode)
	}
}

func TestSignPolicy_Put_happy(t *testing.T) {
	env, _, fakeSigner := newCachePoliciesEnv(t)
	body := `{"auto_sign":true,"key_id":"workspace-default"}`
	resp := env.put(t, "/api/v1/proxy/upstreams/dockerhub/sign-policy", adminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fakeSigner.lastSetReq == nil {
		t.Fatal("signer Set never called")
	}
	if !fakeSigner.lastSetReq.GetAutoSign() || fakeSigner.lastSetReq.GetKeyId() != "workspace-default" {
		t.Errorf("Set payload didn't propagate: %+v", fakeSigner.lastSetReq)
	}
}

func TestScanPolicy_List_returnsRows(t *testing.T) {
	env, fakeScanner, _ := newCachePoliciesEnv(t)
	fakeScanner.scanList = []*scannerv1.ProxyCacheScanPolicy{
		{
			TenantId: testTenantID, UpstreamName: "dockerhub", AutoScan: true, SeverityThreshold: "high",
			UpdatedAt: timestamppb.Now(),
		},
		{
			TenantId: testTenantID, UpstreamName: "ghcr", AutoScan: false, SeverityThreshold: "",
		},
	}
	resp := env.get(t, "/api/v1/proxy/cache/scan-policies", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Policies []struct {
			UpstreamName string `json:"upstream_name"`
		} `json:"policies"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Policies) != 2 {
		t.Errorf("policies len: %d, want 2", len(body.Policies))
	}
}

func TestSignPolicy_List_returnsRows(t *testing.T) {
	env, _, fakeSigner := newCachePoliciesEnv(t)
	fakeSigner.signList = []*signerv1.ProxyCacheSignPolicy{
		{TenantId: testTenantID, UpstreamName: "dockerhub", AutoSign: true, KeyId: "workspace-default"},
	}
	resp := env.get(t, "/api/v1/proxy/cache/sign-policies", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Policies []struct {
			UpstreamName string `json:"upstream_name"`
			AutoSign     bool   `json:"auto_sign"`
			KeyID        string `json:"key_id"`
		} `json:"policies"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Policies) != 1 || body.Policies[0].KeyID != "workspace-default" {
		t.Errorf("list rows didn't propagate: %+v", body.Policies)
	}
}
