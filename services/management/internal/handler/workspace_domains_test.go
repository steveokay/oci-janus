// Tests for FE-API-027 workspace custom-domain CRUD routes. We stand up an
// in-process tenant gRPC fake via bufconn so the handler exercises the real
// HTTP→gRPC→proto path. The fake is intentionally chatty (records every call)
// so we can assert exact wire forwarding.
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ── Domain-aware tenant fake ─────────────────────────────────────────────────

// domainsTenantServer is a bufconn gRPC server with full domain CRUD support.
// Test cases set behaviour via package-level overrides so each route can be
// exercised independently.
type domainsTenantServer struct {
	tenantv1.UnimplementedTenantServiceServer
}

// Overrides — each test resets via t.Cleanup so cases stay isolated.
var (
	dtsListResp          *tenantv1.ListTenantDomainsResponse
	dtsListErr           error
	dtsRegisterResp      *tenantv1.RegisterDomainResponse
	dtsRegisterErr       error
	dtsRegisterReq       *tenantv1.RegisterDomainRequest
	dtsVerifyResp        *tenantv1.DomainEntry
	dtsVerifyErr         error
	dtsSetPrimaryResp    *tenantv1.DomainEntry
	dtsSetPrimaryErr     error
	dtsDeleteErr         error
	dtsDeleteWasPrimary  bool
	dtsLastDeleteRequest *tenantv1.DeleteDomainRequest
)

func (s *domainsTenantServer) ListTenantDomains(_ context.Context, _ *tenantv1.ListTenantDomainsRequest) (*tenantv1.ListTenantDomainsResponse, error) {
	if dtsListErr != nil {
		return nil, dtsListErr
	}
	if dtsListResp != nil {
		return dtsListResp, nil
	}
	return &tenantv1.ListTenantDomainsResponse{}, nil
}

func (s *domainsTenantServer) RegisterDomain(_ context.Context, req *tenantv1.RegisterDomainRequest) (*tenantv1.RegisterDomainResponse, error) {
	dtsRegisterReq = req
	if dtsRegisterErr != nil {
		return nil, dtsRegisterErr
	}
	if dtsRegisterResp != nil {
		return dtsRegisterResp, nil
	}
	return &tenantv1.RegisterDomainResponse{VerificationToken: "tok-aabbccdd"}, nil
}

func (s *domainsTenantServer) VerifyDomainNow(_ context.Context, req *tenantv1.VerifyDomainNowRequest) (*tenantv1.DomainEntry, error) {
	if dtsVerifyErr != nil {
		return nil, dtsVerifyErr
	}
	if dtsVerifyResp != nil {
		return dtsVerifyResp, nil
	}
	return &tenantv1.DomainEntry{
		Domain:       req.GetDomain(),
		Verified:     true,
		IsPrimary:    true,
		RegisteredAt: timestamppb.New(time.Now().Add(-time.Hour)),
		VerifiedAt:   timestamppb.Now(),
	}, nil
}

func (s *domainsTenantServer) SetPrimaryDomain(_ context.Context, req *tenantv1.SetPrimaryDomainRequest) (*tenantv1.DomainEntry, error) {
	if dtsSetPrimaryErr != nil {
		return nil, dtsSetPrimaryErr
	}
	if dtsSetPrimaryResp != nil {
		return dtsSetPrimaryResp, nil
	}
	return &tenantv1.DomainEntry{
		Domain: req.GetDomain(), Verified: true, IsPrimary: true,
		RegisteredAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}, nil
}

func (s *domainsTenantServer) DeleteDomain(ctx context.Context, req *tenantv1.DeleteDomainRequest) (*emptypb.Empty, error) {
	dtsLastDeleteRequest = req
	if dtsDeleteErr != nil {
		return nil, dtsDeleteErr
	}
	if dtsDeleteWasPrimary {
		// Surface the was-primary signal exactly the way the real server does
		// so the BFF's header-mapping path is exercised end-to-end.
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-janus-was-primary", "true"))
	}
	return &emptypb.Empty{}, nil
}

// Other RPCs reuse the dashboard "happy path" so /workspace/me still works
// for the auth wiring (Register only mounts when h.tenant is non-nil).
func (s *domainsTenantServer) GetTenant(_ context.Context, _ *tenantv1.GetTenantRequest) (*tenantv1.Tenant, error) {
	return &tenantv1.Tenant{TenantId: testTenantID, Name: "Acme", Plan: "free", CreatedAt: timestamppb.Now()}, nil
}

// ── Harness ──────────────────────────────────────────────────────────────────

// newDomainsEnv stands up the same fake stack as newWorkspaceEnv but with the
// chatty domain-aware tenant server. Reuses testEnv so .get/.post/.del helpers
// keep working.
func newDomainsEnv(t *testing.T) *testEnv {
	t.Helper()

	startGRPC := func(register func(*grpc.Server)) *bufconn.Listener {
		lis := bufconn.Listen(bufSize)
		srv := grpc.NewServer()
		register(srv)
		healthpb.RegisterHealthServer(srv, &fakeHealthServer{})
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)
		return lis
	}

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

	authLis := startGRPC(func(s *grpc.Server) { authv1.RegisterAuthServiceServer(s, &fakeAuthServer{}) })
	metaLis := startGRPC(func(s *grpc.Server) { metadatav1.RegisterMetadataServiceServer(s, &fakeMetaServer{}) })
	auditLis := startGRPC(func(s *grpc.Server) { auditv1.RegisterAuditServiceServer(s, &fakeAuditServer{}) })
	tenantLis := startGRPC(func(s *grpc.Server) { tenantv1.RegisterTenantServiceServer(s, &domainsTenantServer{}) })

	authConn := dial(authLis)
	metaConn := dial(metaLis)
	auditConn := dial(auditLis)
	tenantConn := dial(tenantLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, "",
		healthpb.NewHealthClient(authConn),
	).WithTenantClient(tenantv1.NewTenantServiceClient(tenantConn))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Reset every override so adjacent tests don't bleed state.
	t.Cleanup(func() {
		dtsListResp = nil
		dtsListErr = nil
		dtsRegisterResp = nil
		dtsRegisterErr = nil
		dtsRegisterReq = nil
		dtsVerifyResp = nil
		dtsVerifyErr = nil
		dtsSetPrimaryResp = nil
		dtsSetPrimaryErr = nil
		dtsDeleteErr = nil
		dtsDeleteWasPrimary = false
		dtsLastDeleteRequest = nil
	})

	return &testEnv{srv: srv}
}

// ── Route disabled when tenant client unwired ────────────────────────────────

// TestWorkspaceDomains_TenantUnset_returns404 keeps the routes opt-in — same
// pattern as /workspace/me. newTestEnv intentionally does NOT call WithTenantClient.
func TestWorkspaceDomains_TenantUnset_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/domains", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ── GET /domains ─────────────────────────────────────────────────────────────

func TestListDomains_HappyPath_returnsList(t *testing.T) {
	now := time.Now()
	verifiedAt := now.Add(-30 * time.Minute)
	dtsListResp = &tenantv1.ListTenantDomainsResponse{
		Domains: []*tenantv1.DomainEntry{
			{
				Domain: "registry.acme.com", Verified: true, IsPrimary: true,
				VerificationToken: "tok-surface-to-admin",
				RegisteredAt:      timestamppb.New(now.Add(-time.Hour)),
				VerifiedAt:        timestamppb.New(verifiedAt),
				NextPollAfter:     timestamppb.New(now.Add(time.Hour)),
			},
		},
	}

	env := newDomainsEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/domains", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var body struct {
		Domains []handler.WorkspaceDomainResponse `json:"domains"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Domains) != 1 {
		t.Fatalf("domains: got %d, want 1", len(body.Domains))
	}
	d := body.Domains[0]
	if d.Domain != "registry.acme.com" || !d.Verified || !d.IsPrimary {
		t.Errorf("entry: got %+v", d)
	}
	if d.VerifiedAt == nil {
		t.Errorf("verified_at: got nil, want populated")
	}
	// DSGN-021: verification token + TXT record name now ride along on the
	// admin-only list so the dashboard can re-display the challenge after
	// the register dialog closes. Reader-role callers still get a 403 (see
	// TestListDomains_ReaderRole_returns403 below), so token disclosure is
	// bounded by the same gate that already protects registration.
	if d.VerificationToken != "tok-surface-to-admin" {
		t.Errorf("verification_token: got %q, want surfaced", d.VerificationToken)
	}
	if d.TXTRecordName != "_registry-verify.registry.acme.com" {
		t.Errorf("txt_record_name: got %q", d.TXTRecordName)
	}
	// Sanity: the strings.Contains-on-raw-JSON assertion that used to live
	// here flipped meaning — it now confirms the token round-trips rather
	// than that it stays absent.
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "tok-surface-to-admin") {
		t.Errorf("verification token missing from response: %s", raw)
	}
}

func TestListDomains_ReaderRole_returns403(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/domains", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── POST /domains ────────────────────────────────────────────────────────────

func TestRegisterDomain_HappyPath_returnsInstructions(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains", adminToken,
		`{"domain":"registry.acme.com"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["txt_record_name"] != "_registry-verify.registry.acme.com" {
		t.Errorf("txt_record_name: got %q", body["txt_record_name"])
	}
	if body["verification_token"] != "tok-aabbccdd" {
		t.Errorf("verification_token: got %q", body["verification_token"])
	}
	if body["instructions"] == "" {
		t.Errorf("instructions: empty")
	}
	if dtsRegisterReq.GetDomain() != "registry.acme.com" {
		t.Errorf("forwarded domain: got %q", dtsRegisterReq.GetDomain())
	}
}

func TestRegisterDomain_InvalidRegex_returns400(t *testing.T) {
	env := newDomainsEnv(t)
	// Underscores not allowed by the FE-API-027 regex.
	resp := env.post(t, "/api/v1/workspace/me/domains", adminToken,
		`{"domain":"bad_domain.example.com"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRegisterDomain_PlatformWildcard_returns400(t *testing.T) {
	// The tenant gRPC server bounces with the exact "platform-managed wildcard"
	// error string so the BFF's tailored mapping is exercised.
	dtsRegisterErr = status.Error(codes.InvalidArgument,
		"cannot register domain within the platform-managed wildcard space")

	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains", adminToken,
		`{"domain":"tenant-a.registry.example.com"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body["error"], "wildcard") {
		t.Errorf("error: got %q, want wildcard message", body["error"])
	}
}

func TestRegisterDomain_AlreadyExists_returns409(t *testing.T) {
	dtsRegisterErr = status.Error(codes.AlreadyExists, "duplicate")
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains", adminToken,
		`{"domain":"registry.acme.com"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

func TestRegisterDomain_ReaderRole_returns403(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains", readerToken,
		`{"domain":"registry.acme.com"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── POST /domains/{domain}/verify ───────────────────────────────────────────

func TestVerifyDomain_HappyPath_returnsUpdatedEntry(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains/registry.acme.com/verify", adminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body handler.WorkspaceDomainResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !body.Verified {
		t.Errorf("verified: got false, want true")
	}
	if body.Domain != "registry.acme.com" {
		t.Errorf("domain: got %q", body.Domain)
	}
}

func TestVerifyDomain_Unknown_returns404(t *testing.T) {
	dtsVerifyErr = status.Error(codes.NotFound, "missing")
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains/missing.example.com/verify", adminToken, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestVerifyDomain_ReaderRole_returns403(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.post(t, "/api/v1/workspace/me/domains/registry.acme.com/verify", readerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── PATCH /domains/{domain} ─────────────────────────────────────────────────

func TestPatchDomain_SetPrimary_HappyPath(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.patch(t, "/api/v1/workspace/me/domains/registry.acme.com", adminToken,
		`{"is_primary":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body handler.WorkspaceDomainResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !body.IsPrimary {
		t.Errorf("is_primary: got false, want true")
	}
}

func TestPatchDomain_Unverified_returns409(t *testing.T) {
	dtsSetPrimaryErr = status.Error(codes.FailedPrecondition,
		"cannot set unverified domain as primary")
	env := newDomainsEnv(t)
	resp := env.patch(t, "/api/v1/workspace/me/domains/pending.acme.com", adminToken,
		`{"is_primary":true}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

func TestPatchDomain_Unknown_returns404(t *testing.T) {
	dtsSetPrimaryErr = status.Error(codes.NotFound, "missing")
	env := newDomainsEnv(t)
	resp := env.patch(t, "/api/v1/workspace/me/domains/missing.example.com", adminToken,
		`{"is_primary":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestPatchDomain_IsPrimaryFalse_returns400(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.patch(t, "/api/v1/workspace/me/domains/registry.acme.com", adminToken,
		`{"is_primary":false}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPatchDomain_ReaderRole_returns403(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.patch(t, "/api/v1/workspace/me/domains/registry.acme.com", readerToken,
		`{"is_primary":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ── DELETE /domains/{domain} ────────────────────────────────────────────────

func TestDeleteDomain_NonPrimary_returns204_noWarning(t *testing.T) {
	dtsDeleteWasPrimary = false
	env := newDomainsEnv(t)
	resp := env.del(t, "/api/v1/workspace/me/domains/extra.acme.com", adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if v := resp.Header.Get("X-Janus-Warning"); v != "" {
		t.Errorf("X-Janus-Warning: got %q, want empty", v)
	}
}

func TestDeleteDomain_Primary_setsWarningHeader(t *testing.T) {
	dtsDeleteWasPrimary = true
	env := newDomainsEnv(t)
	resp := env.del(t, "/api/v1/workspace/me/domains/registry.acme.com", adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if v := resp.Header.Get("X-Janus-Warning"); v != "primary-domain-removed" {
		t.Errorf("X-Janus-Warning: got %q, want primary-domain-removed", v)
	}
	if dtsLastDeleteRequest.GetDomain() != "registry.acme.com" {
		t.Errorf("forwarded domain: got %q", dtsLastDeleteRequest.GetDomain())
	}
}

func TestDeleteDomain_Unknown_returns404(t *testing.T) {
	dtsDeleteErr = status.Error(codes.NotFound, "missing")
	env := newDomainsEnv(t)
	resp := env.del(t, "/api/v1/workspace/me/domains/missing.example.com", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDeleteDomain_ReaderRole_returns403(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.del(t, "/api/v1/workspace/me/domains/registry.acme.com", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}
