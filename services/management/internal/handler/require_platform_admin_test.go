// Tests for the requirePlatformAdmin gate — REDESIGN-001 Phase 5.1.
//
// These tests live in the external package handler_test so they can use the
// bufconn-based fake stack (same pattern as admin_tenants_test.go). The gate
// is exercised by hitting /api/v1/admin/tenants/{id} (a representative admin
// route) with different caller identities and deployment modes.
//
// Four scenarios:
//   - Global admin (is_global_admin=true) → 200 (canonical Phase 5.1 path)
//   - Legacy marker only (admin, org, '*') → 403 (marker no longer honoured)
//   - Single-mode + tenant-admin → 200 (deployment shortcut)
//   - Multi-mode + tenant-admin → 403 (distinction preserved)
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ── Tokens / user IDs for this suite ─────────────────────────────────────────

const (
	// pga5Token is used to represent a user whose is_global_admin=true.
	// The "pga5" prefix signals "platform-global-admin Phase-5.1".
	pga5Token  = "pga5-global-admin-token"
	pga5UserID = "00000000-aaaa-aaaa-aaaa-000000000001"

	// legacyMarkerToken represents a user whose GetUserPermissions returns the
	// legacy (admin, org, '*') marker but is_global_admin=false. After Phase 5.1
	// this must be denied on the global-admin gate.
	legacyMarkerToken  = "pga5-legacy-marker-token"
	legacyMarkerUserID = "00000000-aaaa-aaaa-aaaa-000000000002"

	// tenantAdminToken represents a user with a (tenant, testTenantID, admin)
	// grant — the Phase 5.2 tenant-admin pattern. Used for both single-mode
	// (should pass) and multi-mode (should fail) tests.
	pga5TenantAdminToken  = "pga5-tenant-admin-token"
	pga5TenantAdminUserID = "00000000-aaaa-aaaa-aaaa-000000000003"
)

// ── Fake auth server for Phase 5.1 scenarios ─────────────────────────────────

// pga5FakeAuthServer handles ValidateToken and GetUserPermissions for the
// four Phase 5.1 test identities defined above.
type pga5FakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *pga5FakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case pga5Token:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: pga5UserID}, nil
	case legacyMarkerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: legacyMarkerUserID}, nil
	case pga5TenantAdminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: pga5TenantAdminUserID}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *pga5FakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	switch req.GetUserId() {
	case pga5UserID:
		// Typed is_global_admin flag — the canonical Phase 5.1 path.
		// No legacy (org=*, admin) marker: the typed column alone is sufficient.
		return &authv1.GetUserPermissionsResponse{
			IsGlobalAdmin: true,
			Roles:         []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "pga5-assign", UserId: pga5UserID, Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	case legacyMarkerUserID:
		// Only the legacy (admin, org, '*') marker — is_global_admin=false.
		// After Phase 5.1 effectiveGlobalAdmin must NOT accept this in multi-mode.
		return &authv1.GetUserPermissionsResponse{
			IsGlobalAdmin: false,
			Roles:         []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "legacy-assign", UserId: legacyMarkerUserID, Role: "admin", ScopeType: "org", ScopeValue: "*"},
			},
		}, nil
	case pga5TenantAdminUserID:
		// Tenant-scoped admin — qualifies as platform-admin in single mode only.
		return &authv1.GetUserPermissionsResponse{
			IsGlobalAdmin: false,
			Roles:         []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "tenant-admin-assign", UserId: pga5TenantAdminUserID, Role: "admin", ScopeType: "tenant", ScopeValue: testTenantID},
			},
		}, nil
	default:
		return &authv1.GetUserPermissionsResponse{}, nil
	}
}

// GetTenant is called by the admin-tenants route after the gate passes.
// Return a minimal happy-path tenant so the route completes without 404/500.
func (s *pga5FakeAuthServer) CountTenantUsers(_ context.Context, _ *authv1.CountTenantUsersRequest) (*authv1.CountTenantUsersResponse, error) {
	return &authv1.CountTenantUsersResponse{Count: 1}, nil
}

// ── newPGA5Env ─────────────────────────────────────────────────────────────

// pga5Env holds the full bufconn stack for Phase 5.1 gate tests.
type pga5Env struct {
	srv *httptest.Server
}

// newPGA5Env builds a bufconn stack with pga5FakeAuthServer and a minimal
// tenant/meta/audit server. The platform is single-tenant only (ADR-0031), so
// there is no deployment-mode branch.
func newPGA5Env(t *testing.T) *pga5Env {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &pga5FakeAuthServer{})
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

	// Minimal tenant server so the admin-tenants route can satisfy itself after
	// the gate passes. It doesn't need to return real data — just avoid 500.
	tenantLis := bufconn.Listen(bufSize)
	tenantGRPC := grpc.NewServer()
	tenantv1.RegisterTenantServiceServer(tenantGRPC, &adminFakeTenantServer{})
	healthpb.RegisterHealthServer(tenantGRPC, &fakeHealthServer{})
	go func() { _ = tenantGRPC.Serve(tenantLis) }()
	t.Cleanup(tenantGRPC.Stop)

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
		nil, // no publisher
		"",  // no platformAdminTenantID
		healthpb.NewHealthClient(dial(authLis)),
	).
		WithTenantClient(tenantv1.NewTenantServiceClient(dial(tenantLis))).
		WithDeploymentInfo("test")

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &pga5Env{srv: srv}
}

func (e *pga5Env) get(t *testing.T, path, token string) *http.Response {
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

// ── Scenario tests ────────────────────────────────────────────────────────────

// TestRequirePlatformAdmin_GlobalAdmin_Allowed verifies that a user with
// is_global_admin=true passes the requirePlatformAdmin gate. This is the
// canonical Phase 5.1 acceptance path.
func TestRequirePlatformAdmin_GlobalAdmin_Allowed(t *testing.T) {
	env := newPGA5Env(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, pga5Token)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("global-admin user: expected 200, got %d (is_global_admin=true must pass the gate)", resp.StatusCode)
	}
}

// TestRequirePlatformAdmin_LegacyMarkerOnly_Denied verifies that a user whose
// GetUserPermissions returns only the legacy (admin, org, '*') marker — but
// is_global_admin=false and no tenant-admin role — is denied.
//
// This is the core security regression test for Phase 5.1: the (admin, org, '*')
// convention could previously be minted by anyone who could call GrantRole with
// scope_value='*'. The typed column removes that footgun; the gate must not
// fall back to the legacy org-scoped marker. Note the single-tenant tenant-admin
// shortcut (ADR-0031) does NOT rescue this user — an org-scoped '*' marker is
// not a tenant-scoped admin role.
func TestRequirePlatformAdmin_LegacyMarkerOnly_Denied(t *testing.T) {
	env := newPGA5Env(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, legacyMarkerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("legacy-marker-only user: expected 403, got %d (legacy org marker must be denied after Phase 5.1)", resp.StatusCode)
	}
}

// TestRequirePlatformAdmin_TenantAdmin_Allowed verifies the single-tenant
// shortcut (ADR-0031): any tenant-admin qualifies as platform-admin because the
// tenant IS the deployment. This spares operators from manually setting
// is_global_admin for the bootstrap admin.
func TestRequirePlatformAdmin_TenantAdmin_Allowed(t *testing.T) {
	env := newPGA5Env(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, pga5TenantAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tenant-admin: expected 200, got %d (single-tenant shortcut must qualify any tenant-admin)", resp.StatusCode)
	}
}
