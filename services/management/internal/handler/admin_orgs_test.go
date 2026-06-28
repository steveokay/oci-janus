// Tests for POST /api/v1/admin/orgs/{org}/claim — the platform-admin
// "bootstrap a new org" route that closes the chicken-and-egg where a
// platform admin couldn't create a repo under a fresh org name without first
// running raw SQL.
//
// The test harness intentionally mirrors admin_tenants_test.go: bufconn-backed
// auth server with a token-keyed permission map, and a tiny GrantRole capture
// hook so each case can assert that the right (user, role, scope) tuple was
// forwarded to services/auth.
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// claimFakeAuthServer is a token-aware auth fake scoped to this suite. It
// recognises:
//   - platformAdminToken: holds the (org=*, admin) marker grant
//   - readerToken: holds only a reader grant on "myorg" — must be rejected
//
// GrantRole calls are captured into grantRoleCaptured so tests can assert
// that the right tuple was forwarded.
type claimFakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

var (
	grantRoleCapturedMu sync.Mutex
	grantRoleCaptured   *authv1.GrantRoleRequest
	grantRoleError      error
)

func (s *claimFakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case platformAdminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: platformAdminUser}, nil
	case readerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "reader-user"}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *claimFakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	switch req.GetUserId() {
	case platformAdminUser:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "marker", UserId: platformAdminUser, Role: "admin", ScopeType: "org", ScopeValue: "*"},
			},
		}, nil
	default:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"reader"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "r1", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	}
}

func (s *claimFakeAuthServer) GrantRole(_ context.Context, req *authv1.GrantRoleRequest) (*emptypb.Empty, error) {
	grantRoleCapturedMu.Lock()
	grantRoleCaptured = req
	grantRoleCapturedMu.Unlock()
	if grantRoleError != nil {
		return nil, grantRoleError
	}
	return &emptypb.Empty{}, nil
}

// newClaimEnv stands up just enough of the bufconn stack for the claim
// route: auth server (with GrantRole capture), plus empty metadata + audit
// servers because handler.New requires all three clients. The tenant client
// is left nil — the claim route doesn't touch it.
func newClaimEnv(t *testing.T) *testEnv {
	t.Helper()

	t.Cleanup(func() {
		grantRoleCapturedMu.Lock()
		grantRoleCaptured = nil
		grantRoleError = nil
		grantRoleCapturedMu.Unlock()
	})

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &claimFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	// Reuse adminFakeMetaServer + adminFakeAuditServer — the claim route
	// doesn't call them, but handler.New requires non-nil clients.
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &adminFakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &adminFakeAuditServer{})
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
		healthpb.NewHealthClient(dial(authLis)),
	)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// TestAdminClaimOrg_nonPlatformAdmin_rejected asserts that a caller without
// the (org=*, admin) marker grant is refused with 403, even when they hold
// admin/owner on some other scope. Preserves PENTEST-024.
func TestAdminClaimOrg_nonPlatformAdmin_rejected(t *testing.T) {
	env := newClaimEnv(t)
	resp := env.post(t, "/api/v1/admin/orgs/newco/claim", readerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	grantRoleCapturedMu.Lock()
	defer grantRoleCapturedMu.Unlock()
	if grantRoleCaptured != nil {
		t.Fatalf("GrantRole must NOT have been called for a non-platform-admin caller, got %+v", grantRoleCaptured)
	}
}

// TestAdminClaimOrg_platformAdmin_grantsAdminOnOrg asserts the happy path:
// the marker holder POSTs /admin/orgs/newco/claim, the BFF forwards a
// GrantRole(admin, org, newco) call to services/auth, and 201 comes back
// with the claimed org name in the body.
func TestAdminClaimOrg_platformAdmin_grantsAdminOnOrg(t *testing.T) {
	env := newClaimEnv(t)
	resp := env.post(t, "/api/v1/admin/orgs/newco/claim", platformAdminToken, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var body struct {
		Org         string `json:"org"`
		GrantedRole string `json:"granted_role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Org != "newco" {
		t.Errorf("expected org=newco, got %q", body.Org)
	}
	if body.GrantedRole != "admin" {
		t.Errorf("expected granted_role=admin, got %q", body.GrantedRole)
	}

	grantRoleCapturedMu.Lock()
	defer grantRoleCapturedMu.Unlock()
	if grantRoleCaptured == nil {
		t.Fatal("GrantRole was not called")
	}
	if grantRoleCaptured.GetUserId() != platformAdminUser {
		t.Errorf("expected user_id=%s, got %s", platformAdminUser, grantRoleCaptured.GetUserId())
	}
	if grantRoleCaptured.GetRole() != "admin" {
		t.Errorf("expected role=admin, got %s", grantRoleCaptured.GetRole())
	}
	if grantRoleCaptured.GetScopeType() != "org" {
		t.Errorf("expected scope_type=org, got %s", grantRoleCaptured.GetScopeType())
	}
	if grantRoleCaptured.GetScopeValue() != "newco" {
		t.Errorf("expected scope_value=newco, got %s", grantRoleCaptured.GetScopeValue())
	}
	if grantRoleCaptured.GetTenantId() != testTenantID {
		t.Errorf("expected tenant_id=%s, got %s", testTenantID, grantRoleCaptured.GetTenantId())
	}
	if grantRoleCaptured.GetGrantedBy() != platformAdminUser {
		t.Errorf("expected granted_by=%s, got %s", platformAdminUser, grantRoleCaptured.GetGrantedBy())
	}
}

// TestAdminClaimOrg_idempotent_onExistingOrg asserts that re-claiming the
// same org returns 201 as well — the services/auth role_assignments table
// has ON CONFLICT DO NOTHING so the duplicate grant is silently absorbed.
// This is the "platform admin who didn't admin this org yet" path: GrantRole
// succeeds, the next /repositories call goes through, the existing org row
// (if any) is reused.
func TestAdminClaimOrg_idempotent_onExistingOrg(t *testing.T) {
	env := newClaimEnv(t)

	// First claim — fresh org.
	resp1 := env.post(t, "/api/v1/admin/orgs/existingorg/claim", platformAdminToken, "")
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first claim: expected 201, got %d", resp1.StatusCode)
	}

	// Second claim — duplicate, but services/auth absorbs via ON CONFLICT
	// DO NOTHING. The fake just returns success again to mirror that.
	resp2 := env.post(t, "/api/v1/admin/orgs/existingorg/claim", platformAdminToken, "")
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("re-claim: expected 201, got %d", resp2.StatusCode)
	}

	grantRoleCapturedMu.Lock()
	defer grantRoleCapturedMu.Unlock()
	if grantRoleCaptured == nil || grantRoleCaptured.GetScopeValue() != "existingorg" {
		t.Fatalf("expected re-claim to forward GrantRole with scope_value=existingorg, got %+v", grantRoleCaptured)
	}
}

// TestAdminClaimOrg_invalidOrgName_rejected asserts the validateOrgName gate
// runs first — the literal "*" platform-admin marker cannot leak in here and
// neither can any uppercase / punctuation name. 400 with no GrantRole call.
func TestAdminClaimOrg_invalidOrgName_rejected(t *testing.T) {
	env := newClaimEnv(t)
	resp := env.post(t, "/api/v1/admin/orgs/Bad_Name/claim", platformAdminToken, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	grantRoleCapturedMu.Lock()
	defer grantRoleCapturedMu.Unlock()
	if grantRoleCaptured != nil {
		t.Fatalf("GrantRole must NOT have been called for an invalid org name, got %+v", grantRoleCaptured)
	}
}
