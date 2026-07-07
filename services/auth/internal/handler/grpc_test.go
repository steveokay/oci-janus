// Package handler (grpc_test.go) exercises the gRPC handler for registry-auth.
// Tests use the same in-memory fakes and miniredis approach as http_test.go
// so no real PostgreSQL or Redis is required (CLAUDE.md §18).
package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// buildGRPCHandler is a test helper that creates a GRPCHandler backed by the
// shared in-memory fakes and miniredis. Callers receive the handler plus a
// *testCtx for state manipulation (e.g. creating users, issuing tokens).
func buildGRPCHandler(t *testing.T) (*GRPCHandler, *testCtx) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	return NewGRPCHandler(tc.svc, nil), tc
}

// ── ValidateToken ─────────────────────────────────────────────────────────────

// TestValidateToken_validToken_returnsValidResponse verifies that a freshly
// issued JWT is accepted and that the response fields are correctly populated.
func TestValidateToken_validToken_returnsValidResponse(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New().String()
	userID := uuid.New().String()
	tok := issueTestToken(t, tc.svc, userID, tenantID, []service.RepositoryAccess{
		{Type: "repository", Name: "myorg/myrepo", Actions: []string{"push", "pull"}},
	})

	resp, err := h.ValidateToken(context.Background(), &authv1.ValidateTokenRequest{Token: tok})
	if err != nil {
		t.Fatalf("ValidateToken: unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Error("expected Valid=true")
	}
	if resp.UserId != userID {
		t.Errorf("UserId: got %q, want %q", resp.UserId, userID)
	}
	if resp.TenantId != tenantID {
		t.Errorf("TenantId: got %q, want %q", resp.TenantId, tenantID)
	}
	if resp.Jti == "" {
		t.Error("expected non-empty Jti")
	}
	if len(resp.Access) != 1 {
		t.Fatalf("expected 1 access entry, got %d", len(resp.Access))
	}
	if resp.Access[0].Name != "myorg/myrepo" {
		t.Errorf("access name: got %q, want myorg/myrepo", resp.Access[0].Name)
	}
}

// TestValidateToken_invalidToken_returnsUnauthenticated verifies that a garbage
// token string results in an Unauthenticated gRPC error.
func TestValidateToken_invalidToken_returnsUnauthenticated(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.ValidateToken(context.Background(), &authv1.ValidateTokenRequest{Token: "garbage.token.here"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: got %v, want Unauthenticated", st.Code())
	}
}

// TestValidateToken_revokedToken_returnsUnauthenticated verifies that a revoked
// token is rejected with an Unauthenticated error.
func TestValidateToken_revokedToken_returnsUnauthenticated(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tok := issueTestToken(t, tc.svc, uuid.New().String(), uuid.New().String(), nil)

	// Parse and revoke the token via the service.
	claims, err := tc.svc.ValidateToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("ValidateToken before revocation: %v", err)
	}
	if err := tc.svc.RevokeToken(context.Background(), claims); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	_, err = h.ValidateToken(context.Background(), &authv1.ValidateTokenRequest{Token: tok})
	if err == nil {
		t.Fatal("expected error after revocation, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: got %v, want Unauthenticated", st.Code())
	}
}

// ── ValidateAPIKey ────────────────────────────────────────────────────────────

// TestValidateAPIKey_validKey_returnsValidResponse verifies that a legitimate
// API key is accepted and the response contains the user and tenant identity.
func TestValidateAPIKey_validKey_returnsValidResponse(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New()
	userID := uuid.New()

	// Create an API key via the service.
	key, rawSecret, err := tc.svc.CreateAPIKey(
		context.Background(), tenantID, userID,
		"test-key", []string{"push", "pull"}, nil,
	)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	resp, err := h.ValidateAPIKey(context.Background(), &authv1.ValidateAPIKeyRequest{
		KeyId:     key.ID.String(),
		RawSecret: rawSecret,
	})
	if err != nil {
		t.Fatalf("ValidateAPIKey: unexpected error: %v", err)
	}
	if !resp.Valid {
		t.Error("expected Valid=true")
	}
	if resp.UserId != userID.String() {
		t.Errorf("UserId: got %q, want %q", resp.UserId, userID.String())
	}
	if resp.TenantId != tenantID.String() {
		t.Errorf("TenantId: got %q, want %q", resp.TenantId, tenantID.String())
	}
	if len(resp.Access) == 0 {
		t.Error("expected at least one access entry for scoped API key")
	}
}

// TestValidateAPIKey_invalidKeyID_returnsInvalidArgument verifies that a
// non-UUID key_id is rejected with InvalidArgument.
func TestValidateAPIKey_invalidKeyID_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.ValidateAPIKey(context.Background(), &authv1.ValidateAPIKeyRequest{
		KeyId:     "not-a-uuid",
		RawSecret: "somesecret",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestValidateAPIKey_wrongSecret_returnsUnauthenticated verifies that the
// correct key ID but wrong secret returns Unauthenticated.
func TestValidateAPIKey_wrongSecret_returnsUnauthenticated(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New()
	userID := uuid.New()
	key, _, err := tc.svc.CreateAPIKey(context.Background(), tenantID, userID, "mykey", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	_, err = h.ValidateAPIKey(context.Background(), &authv1.ValidateAPIKeyRequest{
		KeyId:     key.ID.String(),
		RawSecret: "completelywrongsecret",
	})
	if err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: got %v, want Unauthenticated", st.Code())
	}
}

// TestValidateAPIKey_notFound_returnsUnauthenticated verifies that a UUID for
// a non-existent key returns Unauthenticated (not NotFound — avoids oracle attacks).
func TestValidateAPIKey_notFound_returnsUnauthenticated(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.ValidateAPIKey(context.Background(), &authv1.ValidateAPIKeyRequest{
		KeyId:     uuid.New().String(),
		RawSecret: "doesnotmatter",
	})
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: got %v, want Unauthenticated", st.Code())
	}
}

// ── GetUserPermissions ────────────────────────────────────────────────────────

// TestGetUserPermissions_validUser_returnsEmptyAccessAndRoles verifies that a
// valid user ID returns an empty access/roles set (RBAC not yet implemented).
func TestGetUserPermissions_validUser_returnsEmptyAccessAndRoles(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "rbacuser", "Str0ng!Password123")

	resp, err := h.GetUserPermissions(context.Background(), &authv1.GetUserPermissionsRequest{
		UserId:   userID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("GetUserPermissions: unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// Sprint 1: RBAC not implemented — empty lists are the expected response.
	if len(resp.Access) != 0 {
		t.Errorf("expected 0 access entries, got %d", len(resp.Access))
	}
	if len(resp.Roles) != 0 {
		t.Errorf("expected 0 roles, got %d", len(resp.Roles))
	}
}

// TestGetUserPermissions_invalidUserID_returnsInvalidArgument verifies that a
// non-UUID user_id returns InvalidArgument.
func TestGetUserPermissions_invalidUserID_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.GetUserPermissions(context.Background(), &authv1.GetUserPermissionsRequest{
		UserId: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestGetUserPermissions_unknownUser_returnsNotFound verifies that an unknown
// user ID returns a NotFound error.
func TestGetUserPermissions_unknownUser_returnsNotFound(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.GetUserPermissions(context.Background(), &authv1.GetUserPermissionsRequest{
		UserId:   uuid.New().String(),
		TenantId: uuid.New().String(),
	})
	if err == nil {
		t.Fatal("expected error for unknown user, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code: got %v, want NotFound", st.Code())
	}
}

// ── scopesToProto ─────────────────────────────────────────────────────────────

// TestScopesToProto_emptyScopes_returnsNil verifies that an empty scope list
// produces a nil slice (not an empty non-nil slice).
func TestScopesToProto_emptyScopes_returnsNil(t *testing.T) {
	result := scopesToProto(nil)
	if result != nil {
		t.Errorf("expected nil for empty scopes, got %v", result)
	}
	result = scopesToProto([]string{})
	if result != nil {
		t.Errorf("expected nil for empty scopes slice, got %v", result)
	}
}

// ── CountTenantUsers (FE-API-028) ─────────────────────────────────────────────

// TestCountTenantUsers_emptyTenant_returnsZero verifies that a tenant with no
// registered users yields a zero count rather than NotFound or an error.
func TestCountTenantUsers_emptyTenant_returnsZero(t *testing.T) {
	h, _ := buildGRPCHandler(t)
	resp, err := h.CountTenantUsers(context.Background(), &authv1.CountTenantUsersRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("CountTenantUsers: %v", err)
	}
	if resp.GetCount() != 0 {
		t.Errorf("Count: got %d, want 0", resp.GetCount())
	}
}

// TestCountTenantUsers_withUsers_returnsCount verifies that registered users
// contribute to the returned count. The shared fakeUserRepo counts every user
// regardless of tenant — that's fine for this assertion since each test runs
// against a fresh repo via buildGRPCHandler.
func TestCountTenantUsers_withUsers_returnsCount(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	tenantID := uuid.New()
	// Register three users so the count reflects "more than zero".
	registerTestUser(t, tc.svc, tenantID, "u1", "Str0ng!Password123")
	registerTestUser(t, tc.svc, tenantID, "u2", "Str0ng!Password123")
	registerTestUser(t, tc.svc, tenantID, "u3", "Str0ng!Password123")

	resp, err := h.CountTenantUsers(context.Background(), &authv1.CountTenantUsersRequest{
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("CountTenantUsers: %v", err)
	}
	if resp.GetCount() != 3 {
		t.Errorf("Count: got %d, want 3", resp.GetCount())
	}
}

// TestCountTenantUsers_invalidTenantID_returnsInvalidArgument verifies that a
// garbage tenant_id is rejected before reaching the DB.
func TestCountTenantUsers_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandler(t)
	_, err := h.CountTenantUsers(context.Background(), &authv1.CountTenantUsersRequest{
		TenantId: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// ── GetUserPermissions: is_global_admin (REDESIGN-001 Phase 5.1) ──────────────

// TestGetUserPermissions_includes_is_global_admin verifies that when a user's
// is_global_admin flag is true the GetUserPermissions response carries
// IsGlobalAdmin=true. Before Phase 5.1 this field was absent and all callers
// inferred platform-admin status from the (admin, org, '*') role_assignments
// marker — the test encodes the new typed-column contract.
func TestGetUserPermissions_includes_is_global_admin(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	// Register a user via the normal path so the fake repo has them.
	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "globaladmin", "Str0ng!Password123")

	// Directly mark the user as a global admin in the fake repo
	// (mirrors what SetGlobalAdmin/migration would do on a real DB).
	if err := tc.users.SetGlobalAdmin(ctx, userID, true); err != nil {
		t.Fatalf("SetGlobalAdmin in fake: %v", err)
	}

	resp, err := h.GetUserPermissions(ctx, &authv1.GetUserPermissionsRequest{
		UserId:   userID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("GetUserPermissions: %v", err)
	}
	if !resp.GetIsGlobalAdmin() {
		t.Error("expected IsGlobalAdmin=true for a user whose is_global_admin flag was set")
	}
}

// TestGetUserPermissions_is_global_admin_false verifies that a normal user
// (is_global_admin not set) returns IsGlobalAdmin=false — the safe default.
func TestGetUserPermissions_is_global_admin_false(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "regularuser", "Str0ng!Password123")

	resp, err := h.GetUserPermissions(context.Background(), &authv1.GetUserPermissionsRequest{
		UserId:   userID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("GetUserPermissions: %v", err)
	}
	if resp.GetIsGlobalAdmin() {
		t.Error("expected IsGlobalAdmin=false for a regular user")
	}
}

// ── SetGlobalAdmin (REDESIGN-001 Phase 5.1) ───────────────────────────────────

// TestSetGlobalAdmin_grant verifies that SetGlobalAdmin(granted=true) updates
// the user record so a subsequent GetUserPermissions reflects the change.
func TestSetGlobalAdmin_grant(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "tobeadmin", "Str0ng!Password123")
	actorID := uuid.New()

	_, err := h.SetGlobalAdmin(ctx, &authv1.SetGlobalAdminRequest{
		UserId:  userID.String(),
		Granted: true,
		ActorId: actorID.String(),
	})
	if err != nil {
		t.Fatalf("SetGlobalAdmin(grant): %v", err)
	}

	// Verify the flag is now visible through GetUserPermissions.
	resp, err := h.GetUserPermissions(ctx, &authv1.GetUserPermissionsRequest{
		UserId:   userID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("GetUserPermissions after grant: %v", err)
	}
	if !resp.GetIsGlobalAdmin() {
		t.Error("expected IsGlobalAdmin=true after SetGlobalAdmin(granted=true)")
	}
}

// TestSetGlobalAdmin_revoke verifies that SetGlobalAdmin(granted=false) clears
// the is_global_admin flag so subsequent GetUserPermissions returns false.
func TestSetGlobalAdmin_revoke(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "toberevoked", "Str0ng!Password123")

	// Seed the flag directly so we have something to revoke.
	if err := tc.users.SetGlobalAdmin(ctx, userID, true); err != nil {
		t.Fatalf("seed SetGlobalAdmin: %v", err)
	}

	_, err := h.SetGlobalAdmin(ctx, &authv1.SetGlobalAdminRequest{
		UserId:  userID.String(),
		Granted: false,
	})
	if err != nil {
		t.Fatalf("SetGlobalAdmin(revoke): %v", err)
	}

	resp, err := h.GetUserPermissions(ctx, &authv1.GetUserPermissionsRequest{
		UserId:   userID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("GetUserPermissions after revoke: %v", err)
	}
	if resp.GetIsGlobalAdmin() {
		t.Error("expected IsGlobalAdmin=false after SetGlobalAdmin(granted=false)")
	}
}

// TestSetGlobalAdmin_unknownUser_returnsNotFound verifies that a non-existent
// user ID surfaces as a NotFound gRPC error rather than an internal error.
func TestSetGlobalAdmin_unknownUser_returnsNotFound(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.SetGlobalAdmin(context.Background(), &authv1.SetGlobalAdminRequest{
		UserId:  uuid.New().String(),
		Granted: true,
	})
	if err == nil {
		t.Fatal("expected NotFound error for unknown user, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code: got %v, want NotFound", st.Code())
	}
}

// TestSetGlobalAdmin_invalidUserID_returnsInvalidArgument verifies that a
// non-UUID user_id is rejected before touching the repository.
func TestSetGlobalAdmin_invalidUserID_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.SetGlobalAdmin(context.Background(), &authv1.SetGlobalAdminRequest{
		UserId:  "not-a-uuid",
		Granted: true,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for non-UUID user_id, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// ── GrantRole: star-scope guard (REDESIGN-001 Phase 5.1) ─────────────────────

// TestGrantRole_rejects_star_scope verifies that GrantRole now returns
// InvalidArgument when scope_value='*' is passed. This is the mechanical guard
// that prevents any caller from minting the deprecated (admin, org, '*') legacy
// platform-admin marker via the normal role-assignment path.
// Callers must use SetGlobalAdmin instead (REDESIGN-001 Phase 5.1).
func TestGrantRole_rejects_star_scope(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.GrantRole(context.Background(), &authv1.GrantRoleRequest{
		TenantId:   uuid.New().String(),
		UserId:     uuid.New().String(),
		Role:       "admin",
		ScopeType:  "org",
		ScopeValue: "*", // the forbidden value
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for scope_value='*', got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestGrantRole_non_star_scope_succeeds verifies that the star-scope guard
// only fires on scope_value='*' — a regular org scope still works.
func TestGrantRole_non_star_scope_succeeds(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.GrantRole(context.Background(), &authv1.GrantRoleRequest{
		TenantId:   uuid.New().String(),
		UserId:     uuid.New().String(),
		Role:       "admin",
		ScopeType:  "org",
		ScopeValue: "myorg", // a normal scope — must pass the guard
	})
	// The fake repo's GrantRole is a no-op so no error is expected here.
	if err != nil {
		t.Errorf("GrantRole with regular scope: unexpected error %v", err)
	}
}

// ── GrantRole: delegator-dominates-delegatee (REDESIGN-001 Phase 5.3) ────────
//
// The four tests below match the four canonical cases for the rule:
//
//   1. owner-of-org-A grants admin on org-A repos → allowed (rank ok, scope ok)
//   2. admin-of-org-A grants owner on org-A      → denied (rank promotion)
//   3. admin-of-org-A grants admin on org-B      → denied (scope mismatch)
//   4. (the SA-side case lives in service_account_test.go — see
//      TestServiceAccount_Create_DelegationGuard_*)
//
// All three tests seed a real human user in the fake user repo so the handler
// can resolve granted_by → GetUserByID → GetUserRoles; the seeded user is
// NOT a global admin so the delegation check is not bypassed.

// TestGrantRole_owner_of_orgA_can_grant_admin_on_orgA_repo verifies the
// happy path: an org-owner has rank 4, granting "admin" (rank 3) on a repo
// inside the same org passes both the scope-dominance and rank checks.
func TestGrantRole_owner_of_orgA_can_grant_admin_on_orgA_repo(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	// Seed the actor as a real user so GetUserByID succeeds (otherwise the
	// handler falls through to the strict "unknown actor" branch).
	actorID := registerTestUser(t, tc.svc, tenantID, "owneractor", "Str0ng!Password123")
	tc.users.seedRole(actorID, tenantID, "owner", "org", "org-a")

	target := uuid.New().String()
	_, err := h.GrantRole(ctx, &authv1.GrantRoleRequest{
		TenantId:   tenantID.String(),
		UserId:     target,
		Role:       "admin",
		ScopeType:  "repo",
		ScopeValue: "org-a/payments", // child of org-a → dominated by the owner assignment
		GrantedBy:  actorID.String(),
	})
	if err != nil {
		t.Fatalf("GrantRole (owner of org-a granting admin on org-a/payments): unexpected error: %v", err)
	}
}

// TestGrantRole_admin_of_orgA_cannot_grant_owner_on_orgA verifies the
// rank-promotion guard: an admin (rank 3) cannot mint an owner (rank 4) even
// at the same scope. Expected status code: PermissionDenied.
func TestGrantRole_admin_of_orgA_cannot_grant_owner_on_orgA(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	actorID := registerTestUser(t, tc.svc, tenantID, "adminactor1", "Str0ng!Password123")
	tc.users.seedRole(actorID, tenantID, "admin", "org", "org-a")

	target := uuid.New().String()
	_, err := h.GrantRole(ctx, &authv1.GrantRoleRequest{
		TenantId:   tenantID.String(),
		UserId:     target,
		Role:       "owner", // rank 4 > caller's rank 3 → forbidden
		ScopeType:  "org",
		ScopeValue: "org-a",
		GrantedBy:  actorID.String(),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for admin→owner promotion, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code: got %v, want PermissionDenied (admin cannot promote to owner)", st.Code())
	}
}

// TestGrantRole_admin_of_orgA_cannot_grant_admin_on_orgB verifies the
// scope-isolation guard: an admin in org-a cannot grant any role in org-b,
// even at the same rank, because no assignment of theirs dominates org-b.
// Expected status code: PermissionDenied.
func TestGrantRole_admin_of_orgA_cannot_grant_admin_on_orgB(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	actorID := registerTestUser(t, tc.svc, tenantID, "adminactor2", "Str0ng!Password123")
	tc.users.seedRole(actorID, tenantID, "admin", "org", "org-a")

	target := uuid.New().String()
	_, err := h.GrantRole(ctx, &authv1.GrantRoleRequest{
		TenantId:   tenantID.String(),
		UserId:     target,
		Role:       "admin",
		ScopeType:  "org",
		ScopeValue: "org-b", // different org → no assignment dominates
		GrantedBy:  actorID.String(),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for cross-scope grant, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code: got %v, want PermissionDenied (no assignment dominates org-b)", st.Code())
	}
}

// TestGrantRole_globalAdmin_bypasses_delegation_check verifies that a user
// marked is_global_admin=true can grant any role at any scope without holding
// an explicit role_assignments row that would otherwise dominate the target.
// This is the documented escape hatch for platform admins.
func TestGrantRole_globalAdmin_bypasses_delegation_check(t *testing.T) {
	h, tc := buildGRPCHandler(t)
	ctx := context.Background()

	tenantID := uuid.New()
	actorID := registerTestUser(t, tc.svc, tenantID, "platformroot", "Str0ng!Password123")
	if err := tc.users.SetGlobalAdmin(ctx, actorID, true); err != nil {
		t.Fatalf("SetGlobalAdmin: %v", err)
	}
	// Deliberately no role assignments — the global-admin flag alone must
	// be enough to satisfy the delegation guard.

	_, err := h.GrantRole(ctx, &authv1.GrantRoleRequest{
		TenantId:   tenantID.String(),
		UserId:     uuid.New().String(),
		Role:       "owner",
		ScopeType:  "org",
		ScopeValue: "any-org",
		GrantedBy:  actorID.String(),
	})
	if err != nil {
		t.Fatalf("global admin should bypass delegation guard, got: %v", err)
	}
}

// TestScopesToProto_withScopes_wrapsAsWildcardAccess verifies that a non-empty
// scope list is wrapped as a single wildcard RepositoryAccess entry.
func TestScopesToProto_withScopes_wrapsAsWildcardAccess(t *testing.T) {
	scopes := []string{"push", "pull", "delete"}
	result := scopesToProto(scopes)

	if len(result) != 1 {
		t.Fatalf("expected 1 RepositoryAccess, got %d", len(result))
	}
	if result[0].Type != "repository" {
		t.Errorf("Type: got %q, want repository", result[0].Type)
	}
	if result[0].Name != "*" {
		t.Errorf("Name: got %q, want *", result[0].Name)
	}
	if len(result[0].Actions) != len(scopes) {
		t.Errorf("Actions len: got %d, want %d", len(result[0].Actions), len(scopes))
	}
}

// ── ResolveUserEmails (FUT-019 Phase 3) ───────────────────────────────────────
//
// Near-clone of LookupUsernames: batch, dedupe, per-request cap. The handler
// forwards the parsed id set to the repo (via the service), which drops users
// with no email — so the response can be shorter than the request.

// TestResolveUserEmails_invalidTenantID_returnsInvalidArgument verifies a
// non-UUID tenant_id is rejected before any repo call (mirrors LookupUsernames).
func TestResolveUserEmails_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	_, err := h.ResolveUserEmails(context.Background(), &authv1.ResolveUserEmailsRequest{
		TenantId: "not-a-uuid",
		UserIds:  []string{uuid.New().String()},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestResolveUserEmails_emptyIDs_returnsEmpty verifies an empty user_ids set
// short-circuits to an empty response without error (mirrors LookupUsernames).
func TestResolveUserEmails_emptyIDs_returnsEmpty(t *testing.T) {
	h, _ := buildGRPCHandler(t)

	resp, err := h.ResolveUserEmails(context.Background(), &authv1.ResolveUserEmailsRequest{
		TenantId: uuid.New().String(),
		UserIds:  nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEmails()) != 0 {
		t.Errorf("expected empty response, got %d entries", len(resp.GetEmails()))
	}
}

// TestResolveUserEmails_resolvesTwo_omitsEmptyEmail seeds three users in one
// tenant — two with emails, one without — and verifies the handler returns
// exactly the two resolvable addresses. The empty-email user is dropped by the
// repo, matching the "users with no email are omitted" contract.
func TestResolveUserEmails_resolvesTwo_omitsEmptyEmail(t *testing.T) {
	h, tc := buildGRPCHandler(t)

	tenantID := uuid.New()
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	// Seed directly into the fake store (same package) so we control emails.
	tc.users.users["alice"] = &repository.User{ID: id1, TenantID: tenantID, Username: "alice", Email: "alice@example.com"}
	tc.users.users["bob"] = &repository.User{ID: id2, TenantID: tenantID, Username: "bob", Email: "bob@example.com"}
	tc.users.users["carol"] = &repository.User{ID: id3, TenantID: tenantID, Username: "carol", Email: ""} // no email → omitted

	resp, err := h.ResolveUserEmails(context.Background(), &authv1.ResolveUserEmailsRequest{
		TenantId: tenantID.String(),
		UserIds:  []string{id1.String(), id2.String(), id3.String()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEmails()) != 2 {
		t.Fatalf("expected 2 emails, got %d", len(resp.GetEmails()))
	}
	got := map[string]string{}
	for _, e := range resp.GetEmails() {
		got[e.GetUserId()] = e.GetEmail()
	}
	if got[id1.String()] != "alice@example.com" {
		t.Errorf("id1 email: got %q, want alice@example.com", got[id1.String()])
	}
	if got[id2.String()] != "bob@example.com" {
		t.Errorf("id2 email: got %q, want bob@example.com", got[id2.String()])
	}
	if _, present := got[id3.String()]; present {
		t.Errorf("id3 (no email) should have been omitted, got %q", got[id3.String()])
	}
}
