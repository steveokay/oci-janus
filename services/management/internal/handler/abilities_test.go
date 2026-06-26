// Package handler_test — integration tests for GET /api/v1/me/abilities.
//
// REDESIGN-001 Phase 4.4. Follows the bufconn pattern used by all other
// handler integration tests: a real HTTP mux with fake gRPC backends so the
// full auth → handler → JSON path is exercised end-to-end.
//
// Three required scenarios:
//   - Happy path: assignments + is_global_admin=false returned correctly.
//   - Global-admin path: is_global_admin=true flows through to the response.
//   - Auth gRPC error: GetUserPermissions failure → 500.
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
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ---------------------------------------------------------------------------
// Tokens specific to the abilities test suite.
//
// Note: adminToken / readerToken / testTenantID / testUserID are declared in
// handler_test.go (same package) and reused here.
// ---------------------------------------------------------------------------

const (
	// abilitiesGlobalAdminToken identifies the caller whose GetUserPermissions
	// response carries is_global_admin=true — the typed Phase 5.1 flag.
	abilitiesGlobalAdminToken  = "abilities-global-admin-token"
	abilitiesGlobalAdminUserID = "00000000-0000-0000-abcd-000000000001"

	// abilitiesGRPCErrToken identifies a caller whose GetUserPermissions call
	// will return a gRPC Unavailable error, used to test the 500 error path.
	abilitiesGRPCErrToken  = "abilities-grpc-err-token"
	abilitiesGRPCErrUserID = "00000000-0000-0000-abcd-000000000002"
)

// ---------------------------------------------------------------------------
// abilitiesFakeAuthServer — a dedicated auth server for this test suite that
// extends the standard fakeAuthServer with the two new token identities above.
// ---------------------------------------------------------------------------

// abilitiesFakeAuthServer handles ValidateToken and GetUserPermissions for all
// tokens recognised by the abilities test suite. It embeds
// authv1.UnimplementedAuthServiceServer so the compiler is satisfied for any
// methods not overridden (GrantRole, RevokeRole, etc.).
type abilitiesFakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *abilitiesFakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	// Standard tokens from handler_test.go — re-handled here so the abilities
	// suite can share the same auth server without importing the shared fake.
	case adminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: testUserID}, nil
	case readerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "reader-user"}, nil
	// Abilities-suite-specific tokens.
	case abilitiesGlobalAdminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: abilitiesGlobalAdminUserID}, nil
	case abilitiesGRPCErrToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: abilitiesGRPCErrUserID}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *abilitiesFakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	switch req.GetUserId() {
	// testUserID → standard org-admin assignments, is_global_admin=false.
	case testUserID:
		return &authv1.GetUserPermissionsResponse{
			IsGlobalAdmin: false,
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-admin", UserId: testUserID, Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
				{Id: "assign-reader", UserId: testUserID, Role: "reader", ScopeType: "repo", ScopeValue: "myorg/image-a"},
			},
		}, nil
	// Global-admin caller → is_global_admin=true with a tenant-scoped grant.
	case abilitiesGlobalAdminUserID:
		return &authv1.GetUserPermissionsResponse{
			IsGlobalAdmin: true,
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-tenant", UserId: abilitiesGlobalAdminUserID, Role: "admin", ScopeType: "tenant", ScopeValue: testTenantID},
			},
		}, nil
	// gRPC-error caller → simulate auth-service degradation.
	case abilitiesGRPCErrUserID:
		return nil, status.Error(codes.Unavailable, "transport is closing")
	default:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"reader"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-reader", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	}
}

// ---------------------------------------------------------------------------
// newAbilitiesTestEnv — bufconn-backed test server wired with the abilities
// suite's dedicated auth fake. Follows the same pattern as newTestEnv
// (handler_test.go) — separate bufconns for auth, meta, and audit.
// ---------------------------------------------------------------------------

type abilitiesTestEnv struct {
	srv *httptest.Server
}

func newAbilitiesTestEnv(t *testing.T) *abilitiesTestEnv {
	t.Helper()

	// Auth bufconn — uses the abilities-specific fake.
	authLis := bufconn.Listen(1 << 20)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &abilitiesFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	// Metadata bufconn — standard fake is sufficient (abilities doesn't call meta).
	metaLis := bufconn.Listen(1 << 20)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	// Audit bufconn — standard fake.
	auditLis := bufconn.Listen(1 << 20)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	// dialBufconn opens a gRPC client connection to a bufconn listener.
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

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, // publisher — not exercised by abilities tests
		"",  // platformAdminTenantID — not exercised by abilities tests
	)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &abilitiesTestEnv{srv: srv}
}

// getAbilities sends GET /api/v1/me/abilities with the given Bearer token.
func (e *abilitiesTestEnv) getAbilities(t *testing.T, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/me/abilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/me/abilities: %v", err)
	}
	return resp
}

// abilitiesBody is the JSON shape of GET /api/v1/me/abilities — mirrors
// handler.abilitiesResponse (unexported) for assertion in external tests.
type abilitiesBody struct {
	IsGlobalAdmin   bool `json:"is_global_admin"`
	RoleAssignments []struct {
		Role       string `json:"role"`
		ScopeType  string `json:"scope_type"`
		ScopeValue string `json:"scope_value"`
	} `json:"role_assignments"`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandleAbilities_returnsAssignments verifies the happy path: the handler
// returns is_global_admin=false and the correct scoped role assignments for a
// caller with org-admin + repo-reader grants.
// REDESIGN-001 Phase 4.4 — closes Review §C2 + §D3.
func TestHandleAbilities_returnsAssignments(t *testing.T) {
	env := newAbilitiesTestEnv(t)

	// adminToken → testUserID → 2 assignments in the fake auth server.
	resp := env.getAbilities(t, adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json Content-Type, got %q", ct)
	}

	var body abilitiesBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.IsGlobalAdmin {
		t.Error("is_global_admin must be false for org-admin caller")
	}
	if len(body.RoleAssignments) != 2 {
		t.Fatalf("expected 2 role_assignments, got %d", len(body.RoleAssignments))
	}

	// First assignment: org-admin on "myorg".
	first := body.RoleAssignments[0]
	if first.Role != "admin" || first.ScopeType != "org" || first.ScopeValue != "myorg" {
		t.Errorf("unexpected first assignment: %+v", first)
	}

	// Second assignment: repo-reader on "myorg/image-a".
	second := body.RoleAssignments[1]
	if second.Role != "reader" || second.ScopeType != "repo" || second.ScopeValue != "myorg/image-a" {
		t.Errorf("unexpected second assignment: %+v", second)
	}
}

// TestHandleAbilities_GlobalAdmin verifies that is_global_admin=true flows
// through from the auth service's GetUserPermissionsResponse into the JSON
// response. The FE useIsGlobalAdmin() hook gates all /admin/* route surfaces
// on this flag.
// REDESIGN-001 Phase 4.4 — is_global_admin typed column (Phase 5.1).
func TestHandleAbilities_GlobalAdmin(t *testing.T) {
	env := newAbilitiesTestEnv(t)

	resp := env.getAbilities(t, abilitiesGlobalAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body abilitiesBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !body.IsGlobalAdmin {
		t.Error("is_global_admin must be true for global-admin caller")
	}
	// role_assignments must be a non-null array, even when is_global_admin=true.
	if body.RoleAssignments == nil {
		t.Error("role_assignments must not be null")
	}
}

// TestHandleAbilities_AuthGRPCError_500 verifies that when GetUserPermissions
// returns a gRPC error (simulating auth-service degradation), the handler
// responds with 500 and a descriptive error body. Fail-closed: the FE must not
// assume any abilities when the backend is degraded.
// REDESIGN-001 Phase 4.4.
func TestHandleAbilities_AuthGRPCError_500(t *testing.T) {
	env := newAbilitiesTestEnv(t)

	resp := env.getAbilities(t, abilitiesGRPCErrToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if msg, ok := body["error"]; !ok || msg != "permissions lookup failed" {
		t.Errorf("expected error='permissions lookup failed', got %v", body)
	}
}

// TestHandleAbilities_Unauthenticated verifies that the route is protected by
// authMW — a request without a Bearer token receives 401.
// REDESIGN-001 Phase 4.4 — the caller may only see their own abilities.
func TestHandleAbilities_Unauthenticated(t *testing.T) {
	env := newAbilitiesTestEnv(t)

	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me/abilities", nil)
	// Intentionally no Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/me/abilities: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
