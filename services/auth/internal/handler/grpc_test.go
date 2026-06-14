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
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// buildGRPCHandler is a test helper that creates a GRPCHandler backed by the
// shared in-memory fakes and miniredis. Callers receive the handler plus a
// *testCtx for state manipulation (e.g. creating users, issuing tokens).
func buildGRPCHandler(t *testing.T) (*GRPCHandler, *testCtx) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	return NewGRPCHandler(tc.svc), tc
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
		UserId: userID.String(),
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
		UserId: uuid.New().String(),
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
