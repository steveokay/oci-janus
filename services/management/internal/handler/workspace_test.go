// Tests for GET /api/v1/workspace/me (FE-API-009 expanded shape). Uses an
// in-process bufconn TenantService fake wired into the existing testEnv via
// handler.WithTenantClient so we exercise the real HTTP→gRPC→proto path.
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeTenantServer is the bufconn gRPC stub. The default GetTenant payload
// covers the FE-API-007 + FE-API-009 happy path (slug + custom primary host).
// Individual tests override workspaceTenantOverride for variant cases.
type fakeTenantServer struct {
	tenantv1.UnimplementedTenantServiceServer
}

// workspaceTenantOverride lets a single test swap in a custom Tenant payload
// for the /workspace/me handler without redefining the whole fake. Reset via
// t.Cleanup so cases stay isolated.
var workspaceTenantOverride *tenantv1.Tenant

func (s *fakeTenantServer) GetTenant(_ context.Context, _ *tenantv1.GetTenantRequest) (*tenantv1.Tenant, error) {
	if workspaceTenantOverride != nil {
		return workspaceTenantOverride, nil
	}
	return &tenantv1.Tenant{
		TenantId:     testTenantID,
		Name:         "Acme",
		Slug:         "acme",
		Plan:         "enterprise",
		Host:         "registry.acme.com",
		HostIsCustom: true,
		Domains: []*tenantv1.DomainEntry{
			{Domain: "registry.acme.com", Verified: true, IsPrimary: true},
		},
		CreatedAt: timestamppb.Now(),
	}, nil
}

func (s *fakeTenantServer) CreateTenant(_ context.Context, _ *tenantv1.CreateTenantRequest) (*tenantv1.Tenant, error) {
	return &tenantv1.Tenant{TenantId: testTenantID, Name: "x", Plan: "standard", CreatedAt: timestamppb.Now()}, nil
}

func (s *fakeTenantServer) DeleteTenant(_ context.Context, _ *tenantv1.DeleteTenantRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// newWorkspaceEnv mirrors newTestEnv but additionally wires a fake
// TenantService so the workspace endpoint isn't disabled. Returns the running
// test server. Returns a copy of testEnv so existing helpers (.get/.post/.del)
// work unchanged.
func newWorkspaceEnv(t *testing.T) *testEnv {
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

	tenantLis := bufconn.Listen(bufSize)
	tenantGRPC := grpc.NewServer()
	tenantv1.RegisterTenantServiceServer(tenantGRPC, &fakeTenantServer{})
	healthpb.RegisterHealthServer(tenantGRPC, &fakeHealthServer{})
	go func() { _ = tenantGRPC.Serve(tenantLis) }()
	t.Cleanup(tenantGRPC.Stop)

	dialBufconn := func(lis *bufconn.Listener) *grpc.ClientConn {
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

	authConn := dialBufconn(authLis)
	metaConn := dialBufconn(metaLis)
	auditConn := dialBufconn(auditLis)
	tenantConn := dialBufconn(tenantLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, // publisher unused on the workspace route
		"",  // platformAdminTenantID unused here
		healthpb.NewHealthClient(authConn),
	).WithTenantClient(tenantv1.NewTenantServiceClient(tenantConn))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// TestWorkspaceMe_TenantClientUnset_returns404 verifies the route stays
// disabled when management isn't wired to registry-tenant — the same
// opt-in pattern WithSignerClient / WithWebhookClient follow.
func TestWorkspaceMe_TenantClientUnset_returns404(t *testing.T) {
	env := newTestEnv(t) // newTestEnv intentionally does NOT call WithTenantClient
	resp := env.get(t, "/api/v1/workspace/me", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestWorkspaceMe_HappyPath_returnsFullShape covers the FE-API-009 wire shape
// end-to-end: slug + host + host_is_custom + domains[]. The fake returns a
// verified custom primary domain so HostIsCustom must be true.
func TestWorkspaceMe_HappyPath_returnsFullShape(t *testing.T) {
	env := newWorkspaceEnv(t)
	resp := env.get(t, "/api/v1/workspace/me", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.WorkspaceResponse
	decodeJSON(t, resp, &body)

	if body.TenantID != testTenantID {
		t.Errorf("tenant_id: got %q, want %q", body.TenantID, testTenantID)
	}
	if body.Slug != "acme" {
		t.Errorf("slug: got %q, want acme", body.Slug)
	}
	if body.Host != "registry.acme.com" {
		t.Errorf("host: got %q, want registry.acme.com", body.Host)
	}
	if !body.HostIsCustom {
		t.Errorf("host_is_custom: got false, want true")
	}
	if body.Plan != "enterprise" {
		t.Errorf("plan: got %q, want enterprise", body.Plan)
	}
	if len(body.Domains) != 1 {
		t.Fatalf("domains: got %d, want 1", len(body.Domains))
	}
	d := body.Domains[0]
	if d.Domain != "registry.acme.com" || !d.Verified || !d.IsPrimary {
		t.Errorf("domains[0]: got %+v, want verified+primary registry.acme.com", d)
	}
}

// TestWorkspaceMe_WildcardFallback_returnsHostIsCustomFalse covers a tenant
// without any verified custom domain — the gRPC server emits a wildcard
// hostname and HostIsCustom=false, and the BFF must pass that through
// rather than re-deriving it.
func TestWorkspaceMe_WildcardFallback_returnsHostIsCustomFalse(t *testing.T) {
	workspaceTenantOverride = &tenantv1.Tenant{
		TenantId:     testTenantID,
		Name:         "Newco",
		Slug:         "newco",
		Plan:         "standard",
		Host:         "newco.registry.example.com",
		HostIsCustom: false,
		Domains:      nil,
		CreatedAt:    timestamppb.Now(),
	}
	t.Cleanup(func() { workspaceTenantOverride = nil })

	env := newWorkspaceEnv(t)
	resp := env.get(t, "/api/v1/workspace/me", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.WorkspaceResponse
	decodeJSON(t, resp, &body)
	if body.HostIsCustom {
		t.Errorf("host_is_custom: got true, want false")
	}
	if body.Host != "newco.registry.example.com" {
		t.Errorf("host: got %q, want wildcard newco.registry.example.com", body.Host)
	}
	// The frontend treats null as a hard error — domains must be an empty
	// array even when the tenant has none.
	if body.Domains == nil {
		t.Errorf("domains: got nil, want non-nil empty slice")
	}
	if len(body.Domains) != 0 {
		t.Errorf("domains: got %d entries, want 0", len(body.Domains))
	}
}

// TestWorkspaceMe_NoAuth_returns401 keeps the route behind RequireAuth — the
// tenant id is derived from the JWT, so an anonymous call has no identity.
func TestWorkspaceMe_NoAuth_returns401(t *testing.T) {
	env := newWorkspaceEnv(t)
	resp := env.get(t, "/api/v1/workspace/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}
