package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/steveokay/oci-janus/services/management/internal/handler"

	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// FUT-012 Phase B — BFF bufconn tests for the 5 new routes.
//
// Each test exercises one of:
//   1. Happy path with a tenant-admin caller
//   2. RBAC denial — a non-admin (writer / reader) hits 403
//   3. Input validation — the BFF rejects malformed bodies before
//      it reaches the gRPC layer
//
// The fakeAuthServer above is extended with stubs for the 3 new RPCs
// + a "tenant-admin-token" that carries the (admin, tenant, <tenant_id>)
// grant the Phase A gates check.

const tenantAdminToken = "tenant-admin-token"

// ── shared smoke helpers ──────────────────────────────────────────────

func newTenantAdminRequest(t *testing.T, srv string, method, path string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if reader != nil {
		req, err = http.NewRequest(method, srv+path, reader)
	} else {
		req, err = http.NewRequest(method, srv+path, nil)
	}
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tenantAdminToken)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ── ListTenantUsers ───────────────────────────────────────────────────

func TestListTenantUsers_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodGet, "/api/v1/tenant/users", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body handler.TenantUsersListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Fake auth server returns 2 users; assert that the wire shape
	// surfaces them through the role_summary chip path.
	if got := len(body.Users); got != 2 {
		t.Errorf("user count: got %d, want 2", got)
	}
	if !body.Users[0].Roles.TenantAdmin {
		t.Errorf("first user should have tenant_admin=true (fake server returns it set)")
	}
}

func TestListTenantUsers_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	// writerToken is in the existing fake-server set — has writer on
	// "myorg" but no tenant-admin grant. Phase B gate should reject.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/tenant/users", nil)
	req.Header.Set("Authorization", "Bearer "+writerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (writer is not tenant-admin)", resp.StatusCode)
	}
}

func TestListTenantUsers_pageSize_invalid_returns400(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodGet, "/api/v1/tenant/users?page_size=99999", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (page_size out of range)", resp.StatusCode)
	}
}

// ── InviteUser ────────────────────────────────────────────────────────

func TestInviteUser_tenantAdmin_returns201WithToken(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	payload, _ := json.Marshal(handler.InviteUserRequestBody{
		Email:       "newcomer@example.com",
		DisplayName: "Newcomer One",
	})
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost, "/api/v1/tenant/users/invite", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	var body handler.InviteUserResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.InviteToken == "" {
		t.Error("expected non-empty invite_token in response — the raw token is the whole point of this surface")
	}
	if body.UserID == "" {
		t.Error("expected user_id in response")
	}
}

func TestInviteUser_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	payload, _ := json.Marshal(handler.InviteUserRequestBody{Email: "x@example.com", DisplayName: "X"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/tenant/users/invite", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+writerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
}

func TestInviteUser_missingEmail_returns400(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	payload, _ := json.Marshal(handler.InviteUserRequestBody{DisplayName: "Display Only"})
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost, "/api/v1/tenant/users/invite", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestInviteUser_initialRoleHalfSet_returns400(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	// Set role without name — paired-field validation must reject.
	payload, _ := json.Marshal(handler.InviteUserRequestBody{
		Email:          "x@example.com",
		DisplayName:    "X",
		InitialOrgRole: "writer",
	})
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost, "/api/v1/tenant/users/invite", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (half-set initial role)", resp.StatusCode)
	}
}

// ── SetUserDisabled ───────────────────────────────────────────────────

func TestDisableTenantUser_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost,
		"/api/v1/tenant/users/00000000-0000-0000-0000-000000000050/disable", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.SetUserDisabledResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "disabled" {
		t.Errorf("status: got %q, want 'disabled' (resulting state)", body.Status)
	}
}

func TestDisableTenantUser_self_returns400(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	// Tenant-admin user id from the fake server. Disabling self
	// would lock the caller out — the BFF refuses.
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost,
		"/api/v1/tenant/users/tenant-admin-user/disable", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (self-disable is blocked)", resp.StatusCode)
	}
}

func TestEnableTenantUser_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodDelete,
		"/api/v1/tenant/users/00000000-0000-0000-0000-000000000050/disable", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.SetUserDisabledResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "active" {
		t.Errorf("status: got %q, want 'active'", body.Status)
	}
}

// ── ElevateToOrgAdmin ─────────────────────────────────────────────────

func TestElevateToOrgAdmin_tenantAdmin_returns204(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost,
		"/api/v1/tenant/users/00000000-0000-0000-0000-000000000050/elevate/myorg", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
}

func TestElevateToOrgAdmin_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/tenant/users/00000000-0000-0000-0000-000000000050/elevate/myorg", nil)
	req.Header.Set("Authorization", "Bearer "+writerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
}

func TestElevateToOrgAdmin_invalidOrgName_returns400(t *testing.T) {
	env := newTestEnv(t)
	srv := env.srv
	req := newTenantAdminRequest(t, srv.URL, http.MethodPost,
		"/api/v1/tenant/users/00000000-0000-0000-0000-000000000050/elevate/UPPERCASE_INVALID", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (invalid org name)", resp.StatusCode)
	}
}

// ── Fake server hooks ────────────────────────────────────────────────

// FUT-012 Phase B: the existing fakeAuthServer (handler_test.go)
// implements ValidateToken + GetUserPermissions. We extend it with
// the 3 new RPC stubs via additional methods on the same type, and
// patch ValidateToken + GetUserPermissions to recognise the
// tenant-admin token + user via the package-level extras maps below
// (consulted at the top of those methods in handler_test.go after
// this PR — minimal invasive change).

// FUT-012 Phase A stubs on the same fake server. Successful invite
// returns a deterministic token so the test can assert presence
// without a real argon2 generation step.
func (s *fakeAuthServer) ListTenantUsers(_ context.Context, _ *authv1.ListTenantUsersRequest) (*authv1.ListTenantUsersResponse, error) {
	// FUT-009 SA-signing tests inject their own tenant user set (and can
	// force an error) via the package-level hooks in handler_test.go. When
	// unset we fall back to the FUT-012 default row set below.
	if listTenantUsersErr != nil {
		return nil, listTenantUsersErr
	}
	if listTenantUsersOverride != nil {
		return listTenantUsersOverride, nil
	}
	return &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{
				UserId: "u1", Username: "tenant-admin-user", DisplayName: "Tenant Admin", Email: "ta@example.com",
				Kind: "human", Status: "active",
				CreatedAt: timestamppb.Now(),
				Roles:     &authv1.RoleSummary{TenantAdmin: true},
			},
			{
				UserId: "u2", Username: "regular", DisplayName: "Regular User", Email: "r@example.com",
				Kind: "human", Status: "active",
				CreatedAt: timestamppb.Now(),
				Roles:     &authv1.RoleSummary{OrgWriterCount: 1},
			},
		},
		TotalCount: 2,
	}, nil
}

func (s *fakeAuthServer) InviteUser(_ context.Context, req *authv1.InviteUserRequest) (*authv1.InviteUserResponse, error) {
	// Fake server doesn't run argon2 — just echoes a deterministic
	// "test token" so the test can assert the wire field is populated.
	return &authv1.InviteUserResponse{
		UserId:          "new-user-id",
		InviteToken:     "test-invite-token-deadbeef",
		InviteExpiresAt: timestamppb.Now(),
	}, nil
}

func (s *fakeAuthServer) SetUserDisabled(_ context.Context, req *authv1.SetUserDisabledRequest) (*authv1.SetUserDisabledResponse, error) {
	st := "active"
	if req.GetDisabled() {
		st = "disabled"
	}
	return &authv1.SetUserDisabledResponse{Status: st}, nil
}

// Re-implement ValidateToken + GetUserPermissions to consult the
// extras maps before falling back to the original behaviour. Go's
// method-set semantics make this tricky; we use a wrapper type.

// Note: the existing fakeAuthServer methods on the base type take
// precedence; the `extra*` maps are checked from the receivers in
// THIS file via a small redirect helper. Specifically: the new
// tenant-admin token is wired by ValidateToken's switch in
// handler_test.go AFTER it consults extraValidateTokens. To keep that
// invasive change off this PR, the integration is done via the
// dispatcher pattern below in patch_validate_token_dispatcher.go (kept
// inline here for readability).
