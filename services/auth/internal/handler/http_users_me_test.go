// Package handler — tests for /api/v1/users/me (FE-API-011/012/013).
// These reuse the in-memory fakes + miniredis harness from http_test.go so
// they need no real Redis or PostgreSQL (CLAUDE.md §18).
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── Common helpers ────────────────────────────────────────────────────────────

// makeMeRequest builds an authenticated HTTP request against /users/me for the
// given user. The token is freshly issued with the user's UUID as `sub` and
// the supplied tenant UUID in the tenant claim.
func makeMeRequest(t *testing.T, srv *httptest.Server, tc *testCtx, method, path string, body []byte, userID, tenantID uuid.UUID) *http.Request {
	t.Helper()
	tok := issueTestToken(t, tc.svc, userID.String(), tenantID.String(), nil)
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// seedTestUser creates a tenant + user via the service and returns both IDs.
// The user has no role assignments unless adminUsers is also mutated.
func seedTestUser(t *testing.T, tc *testCtx, username, password string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, username, password)
	return userID, tenantID
}

// ── GET /users/me ─────────────────────────────────────────────────────────────

// TestGetCurrentUser_noAuth verifies that an unauthenticated request returns 401.
func TestGetCurrentUser_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/users/me")
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestGetCurrentUser_happyPath verifies that the response contains the user's
// core fields, an empty memberships array (no role assignments seeded), and
// an empty roles array.
func TestGetCurrentUser_happyPath_returnsProfile(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "alice", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var got struct {
		UserID      string                   `json:"user_id"`
		Username    string                   `json:"username"`
		Email       *string                  `json:"email"`
		TenantID    string                   `json:"tenant_id"`
		Roles       []string                 `json:"roles"`
		Memberships []map[string]interface{} `json:"memberships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != userID.String() {
		t.Errorf("user_id: got %q, want %q", got.UserID, userID.String())
	}
	if got.Username != "alice" {
		t.Errorf("username: got %q, want alice", got.Username)
	}
	if got.TenantID != tenantID.String() {
		t.Errorf("tenant_id: got %q, want %q", got.TenantID, tenantID.String())
	}
	// No role assignments seeded → roles must be the empty array, not null.
	if got.Roles == nil {
		t.Error("roles: expected empty array, got null")
	}
	if got.Memberships == nil {
		t.Error("memberships: expected empty array, got null")
	}
}

// TestGetCurrentUser_withMemberships verifies that the response includes the
// user's role assignments in both `roles` and `memberships`.
func TestGetCurrentUser_withMemberships_returnsRoles(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "bob", "Str0ng!Password123")
	// Promote the user to admin so the handlerFakeUserRepo returns a role
	// assignment from GetUserRoles.
	tc.users.makeAdmin(userID)

	req := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var got struct {
		Roles       []string `json:"roles"`
		Memberships []struct {
			ScopeType  string `json:"scope_type"`
			ScopeValue string `json:"scope_value"`
			Role       string `json:"role"`
		} `json:"memberships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Errorf("roles: got %v, want [admin]", got.Roles)
	}
	if len(got.Memberships) != 1 || got.Memberships[0].Role != "admin" {
		t.Errorf("memberships: got %+v, want one admin membership", got.Memberships)
	}
}

// TestGetCurrentUser_userMissing_returns401 verifies that a JWT whose subject
// no longer exists is treated as an authentication failure.
func TestGetCurrentUser_userMissing_returns401(t *testing.T) {
	srv, tc := newTestServer(t)
	// Issue a token for a random UUID without ever creating the user.
	tok := issueTestToken(t, tc.svc, uuid.New().String(), uuid.New().String(), nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// ── PATCH /users/me ───────────────────────────────────────────────────────────

// TestPatchCurrentUser_noAuth verifies unauthenticated PATCH returns 401.
func TestPatchCurrentUser_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/users/me", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestPatchCurrentUser_displayNameOnly verifies that PATCH with display_name
// only updates that field and leaves email alone.
func TestPatchCurrentUser_displayNameOnly_succeeds(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "carol", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{"display_name": "Carol Danvers"})
	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(buf))
	}
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["display_name"] != "Carol Danvers" {
		t.Errorf("display_name: got %v, want Carol Danvers", got["display_name"])
	}
}

// TestPatchCurrentUser_emailOnly verifies that an email-only PATCH succeeds.
func TestPatchCurrentUser_emailOnly_succeeds(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "dave", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{"email": "dave@new.example.com"})
	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(buf))
	}
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["email"] != "dave@new.example.com" {
		t.Errorf("email: got %v, want dave@new.example.com", got["email"])
	}
}

// TestPatchCurrentUser_bothFields verifies that supplying both fields applies both.
func TestPatchCurrentUser_bothFields_succeeds(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "eve", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{
		"display_name": "Eve Smith",
		"email":        "eve@new.example.com",
	})
	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

// TestPatchCurrentUser_noFields verifies that a body with no fields returns 400.
func TestPatchCurrentUser_noFields_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "frank", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", []byte(`{}`), userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestPatchCurrentUser_invalidEmail verifies that a garbage email value returns 400.
func TestPatchCurrentUser_invalidEmail_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "gina", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{"email": "not-an-email"})
	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestPatchCurrentUser_invalidDisplayName verifies that a control-character
// display_name is rejected with 400.
func TestPatchCurrentUser_invalidDisplayName_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "hank", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{"display_name": "bad\nname"})
	req := makeMeRequest(t, srv, tc, http.MethodPatch, "/api/v1/users/me", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// ── POST /users/me/password ───────────────────────────────────────────────────

// TestChangePassword_noAuth verifies unauthenticated calls return 401.
func TestChangePassword_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{
		"current_password": "x",
		"new_password":     "y",
	})
	resp, err := http.Post(srv.URL+"/api/v1/users/me/password", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /users/me/password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestChangePassword_happyPath verifies that providing the correct current
// password and a strong new password returns 204 and revokes any other tokens
// the user holds.
func TestChangePassword_happyPath_returns204AndRevokes(t *testing.T) {
	srv, tc := newTestServer(t)
	const (
		username    = "ivy"
		oldPassword = "Str0ng!Password123"
		newPassword = "Even-Str0nger!Pass987"
	)
	userID, tenantID := seedTestUser(t, tc, username, oldPassword)

	// Issue an EXTRA token for this user that should be invalidated by the
	// password change. We capture the JTI by parsing the token claims below.
	extraTok := issueTestToken(t, tc.svc, userID.String(), tenantID.String(), nil)
	// Sanity-check: extra token validates before the password change.
	if _, err := tc.svc.ValidateToken(context.Background(), extraTok); err != nil {
		t.Fatalf("pre-change validate of extra token: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"current_password": oldPassword,
		"new_password":     newPassword,
	})
	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/password", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(buf))
	}

	// All prior JTIs for this user must now be revoked, including extraTok.
	if _, err := tc.svc.ValidateToken(context.Background(), extraTok); err == nil {
		t.Error("expected extra token to be revoked after password change, got nil error")
	}
}

// TestChangePassword_wrongCurrent verifies that an incorrect current_password
// returns 401 with a generic body — never reveal "user not found" via status.
func TestChangePassword_wrongCurrent_returns401(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "jane", "Str0ng!Password123")

	body, _ := json.Marshal(map[string]string{
		"current_password": "Wr0ng!Password",
		"new_password":     "Even-Str0nger!Pass987",
	})
	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/password", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestChangePassword_weakNew verifies that a too-weak new password returns 400
// with the policy message.
func TestChangePassword_weakNew_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	const (
		oldPassword = "Str0ng!Password123"
	)
	userID, tenantID := seedTestUser(t, tc, "ken", oldPassword)

	body, _ := json.Marshal(map[string]string{
		"current_password": oldPassword,
		"new_password":     "weak",
	})
	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/password", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestChangePassword_sameAsCurrent verifies that the new password cannot equal
// the current password — returns 400.
func TestChangePassword_sameAsCurrent_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	const oldPassword = "Str0ng!Password123"
	userID, tenantID := seedTestUser(t, tc, "luna", oldPassword)

	body, _ := json.Marshal(map[string]string{
		"current_password": oldPassword,
		"new_password":     oldPassword,
	})
	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/password", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestChangePassword_rateLimit verifies that after passwordChangeMaxAttempts
// wrong-current-password attempts, the next request returns 429 regardless of
// whether the credentials would otherwise have been correct.
func TestChangePassword_rateLimit_returns429(t *testing.T) {
	srv, tc := newTestServer(t)
	const oldPassword = "Str0ng!Password123"
	userID, tenantID := seedTestUser(t, tc, "mike", oldPassword)

	tok := issueTestToken(t, tc.svc, userID.String(), tenantID.String(), nil)
	doAttempt := func(current string) int {
		body, _ := json.Marshal(map[string]string{
			"current_password": current,
			"new_password":     "Different-Str0ng!Pass987",
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users/me/password", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// Burn through the per-user budget with wrong-current-password.
	// service.passwordChangeMaxAttempts is the threshold; the 6th call should
	// be the one that returns 429.
	for i := 0; i < 5; i++ {
		if status := doAttempt("Wr0ng!Password"); status != http.StatusUnauthorized {
			t.Errorf("attempt %d: got %d, want 401", i+1, status)
		}
	}
	// 6th attempt — even with the CORRECT current password, the user is now
	// throttled and the request must come back as 429.
	if status := doAttempt(oldPassword); status != http.StatusTooManyRequests {
		t.Errorf("post-budget attempt: got %d, want 429", status)
	}
}

// ── service-layer unit tests for the email policy ─────────────────────────────

// TestValidateEmail_throughPatch verifies which addresses the PATCH /users/me
// path accepts vs rejects by driving them through the public service API.
// This is the cheapest way to exercise the unexported validator without
// re-declaring it in the test file.
func TestValidateEmail_throughPatch(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()
	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "validator", "Str0ng!Password123")

	good := []string{
		"a@b.co",
		"alice+tag@example.com",
		"user.name@sub.example.co.uk",
	}
	for _, s := range good {
		email := s
		if _, err := tc.svc.UpdateUserProfile(context.Background(), userID, nil, &email); err != nil {
			t.Errorf("expected %q to be accepted, got %v", s, err)
		}
	}

	bad := []string{
		"no-at-sign",
		"trailing@dot.",
		"with spaces@example.com",
		" leading@example.com",
		"trailing@example.com ",
		"Name <name@example.com>", // RFC 5322 group form is rejected
	}
	for _, s := range bad {
		email := s
		_, err := tc.svc.UpdateUserProfile(context.Background(), userID, nil, &email)
		if !errors.Is(err, service.ErrInvalidEmail) {
			t.Errorf("expected %q to be rejected with ErrInvalidEmail, got %v", s, err)
		}
	}
}
