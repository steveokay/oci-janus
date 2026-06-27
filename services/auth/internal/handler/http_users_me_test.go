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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
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

// ── REDESIGN-001 Phase 4.3 — onboarding-complete flag + endpoint ──────────────

// TestGetCurrentUser_OnboardingFlagPresent verifies that the GET /users/me
// response exposes the new onboarding_complete field. A fresh user has not
// completed onboarding so the value must be false (and the field must be
// present — never elided).
//
// REDESIGN-001 Phase 4.3 §1: this is the regression guard for the FE contract.
func TestGetCurrentUser_OnboardingFlagPresent(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "owen", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	// Decode into a generic map so we can assert presence (not just value)
	// of the new field — a missing field would deserialise to the zero value
	// in a typed struct, hiding the bug.
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	flag, present := got["onboarding_complete"]
	if !present {
		t.Fatal("onboarding_complete: field must be present in /users/me response")
	}
	if flag != false {
		t.Errorf("onboarding_complete: got %v, want false (new user)", flag)
	}
}

// TestCompleteOnboarding_Human_204 verifies the happy path: a human user can
// flip their onboarding flag, the endpoint returns 204, and the repo state is
// updated. We then GET /users/me to confirm the flag is now true end-to-end.
func TestCompleteOnboarding_Human_204(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "paula", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/onboarding/complete", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST onboarding/complete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 204, body=%s", resp.StatusCode, string(buf))
	}

	// Re-fetch /users/me and assert the flag is now true. This double-checks
	// that the handler+repo wiring actually persisted the change rather than
	// returning 204 without mutating state.
	getReq := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me", nil, userID, tenantID)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer getResp.Body.Close()
	var got map[string]interface{}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["onboarding_complete"] != true {
		t.Errorf("onboarding_complete after POST: got %v, want true", got["onboarding_complete"])
	}
}

// TestCompleteOnboarding_Idempotent verifies that calling the endpoint twice
// still returns 204. Wizard "Done" buttons can be double-clicked and clients
// may retry on network blips; the second call MUST NOT error.
func TestCompleteOnboarding_Idempotent(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "quinn", "Str0ng!Password123")

	// First call — sets the flag.
	req1 := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/onboarding/complete", nil, userID, tenantID)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first POST onboarding/complete: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusNoContent {
		t.Fatalf("first call status: got %d, want 204", resp1.StatusCode)
	}

	// Second call — must also return 204, not an error.
	req2 := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/onboarding/complete", nil, userID, tenantID)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second POST onboarding/complete: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		buf, _ := io.ReadAll(resp2.Body)
		t.Fatalf("second call status: got %d, want 204, body=%s", resp2.StatusCode, string(buf))
	}
}

// TestCompleteOnboarding_Unauthenticated_401 verifies that an unauthenticated
// request (no Bearer header) returns 401. We test the no-header case rather
// than a malformed token because the requireAuth path treats both the same.
func TestCompleteOnboarding_Unauthenticated_401(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users/me/onboarding/complete", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST onboarding/complete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestCompleteOnboarding_ServiceAccount_403 verifies that a service-account
// shadow user attempting to complete onboarding is rejected with 403. SAs are
// machine identities and the wizard is a human-only affordance — the handler
// returns FORBIDDEN to signal "wrong principal kind" (the caller IS
// authenticated, they just can't onboard).
func TestCompleteOnboarding_ServiceAccount_403(t *testing.T) {
	env := newSATestEnv(t)

	// Seed an SA + its shadow user in the in-memory fakes, mirroring the
	// existing SA test setup pattern.
	saShadowUserID := uuid.New()
	sa := &repository.ServiceAccount{
		ID:            uuid.New(),
		TenantID:      env.tenantID,
		ShadowUserID:  saShadowUserID,
		Name:          "ci-onboarding-test",
		Description:   "must not be able to onboard",
		AllowedScopes: []string{"pull"},
		CreatedAt:     time.Now(),
	}
	env.saRepo.accounts[sa.ID] = sa
	shadow := &repository.User{
		ID:       saShadowUserID,
		TenantID: env.tenantID,
		Username: "sa-" + sa.ID.String()[:8],
		Email:    "sa+" + sa.ID.String() + "@internal.invalid",
		Kind:     "service_account",
		IsActive: true,
	}
	env.tc.users.users[shadow.Username] = shadow

	// Issue a JWT as the shadow user.
	tok := issueTestToken(t, env.tc.svc, saShadowUserID.String(), env.tenantID.String(), nil)
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/users/me/onboarding/complete", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST onboarding/complete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 403, body=%s", resp.StatusCode, string(buf))
	}

	// Confirm the shadow user's flag was NOT flipped — SA principals must
	// not be able to mutate state through this endpoint even by accident.
	if shadow.OnboardingComplete {
		t.Error("shadow user OnboardingComplete was flipped despite 403")
	}
}

// ── FE-API-048 T16: polymorphic principal envelope ────────────────────────────

// TestUsersMe_HumanCallerKeepsExistingShape verifies that a human caller's
// GET /users/me response retains all existing fields and gains the additive
// "type":"user" field (FE-API-048 T16). The shape change is backwards-compatible:
// existing clients that do not read "type" are unaffected.
func TestUsersMe_HumanCallerKeepsExistingShape(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "alice-t16", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	// Decode into a generic map so we can assert both the existing fields and the
	// new "type" field without coupling to the struct layout.
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Existing fields must still be present (regression guard).
	if got["user_id"] != userID.String() {
		t.Errorf("user_id: got %v, want %s", got["user_id"], userID)
	}
	if got["username"] != "alice-t16" {
		t.Errorf("username: got %v, want alice-t16", got["username"])
	}
	if got["tenant_id"] != tenantID.String() {
		t.Errorf("tenant_id: got %v, want %s", got["tenant_id"], tenantID)
	}
	// roles must be the empty array, not null — existing contract.
	if got["roles"] == nil {
		t.Error("roles: expected empty array, got null")
	}

	// New T16 field: type must be "user" for human callers.
	if got["type"] != "user" {
		t.Errorf("type: got %v, want %q", got["type"], "user")
	}

	// service_account field must be absent for human callers.
	if _, present := got["service_account"]; present {
		t.Error("service_account: field must be absent for human callers")
	}
}

// TestUsersMe_SAKeyCallerGetsPrincipalEnvelope verifies that when the JWT
// belongs to a service-account shadow user (kind="service_account"), GET
// /users/me returns the sanitised saCallerResponse envelope (FE-API-048 T16
// spec §5.6):
//   - "type" == "service_account"
//   - "email" is null (synthetic email must never be leaked)
//   - "display_name" equals the SA name
//   - "service_account" nested object carries id, name, description, allowed_scopes
func TestUsersMe_SAKeyCallerGetsPrincipalEnvelope(t *testing.T) {
	// Use the SA-wired test server (has h.saService != nil). newSATestEnv
	// builds the same httptest.Server shape as newTestServer but also wires
	// the ServiceAccountService via WithServiceAccountService.
	env := newSATestEnv(t)

	// Seed an admin user to satisfy the actor requirement for issueAdminToken.
	adminTok, adminID := env.issueAdminToken(t)
	_ = adminTok // not used directly for the /users/me call

	// Seed a service account directly into the fake SA repo.
	sa := &repository.ServiceAccount{
		ID:            uuid.New(),
		TenantID:      env.tenantID,
		ShadowUserID:  uuid.New(), // will be registered as the shadow user below
		Name:          "ci-prod",
		Description:   "GitHub Actions deploy bot for myapp",
		AllowedScopes: []string{"pull", "push"},
		CreatedBy:     &adminID,
		CreatedAt:     time.Now(),
	}
	env.saRepo.accounts[sa.ID] = sa

	// Register the shadow user in the fake user repo so the JWT lookup succeeds.
	// The shadow user must have kind="service_account" so getCurrentUser branches
	// into the SA path.
	shadowUser := &repository.User{
		ID:       sa.ShadowUserID,
		TenantID: env.tenantID,
		Username: "sa-" + sa.ID.String()[:8],
		Email:    "sa+" + sa.ID.String() + "@internal.invalid",
		Kind:     "service_account",
		IsActive: true,
	}
	env.tc.users.users[shadowUser.Username] = shadowUser

	// Issue a JWT as the shadow user (same mechanism as production API-key auth).
	tok := issueTestToken(t, env.tc.svc, sa.ShadowUserID.String(), env.tenantID.String(), nil)

	req, err := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/users/me", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(body))
	}

	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Top-level shape assertions.
	if got["type"] != "service_account" {
		t.Errorf("type: got %v, want %q", got["type"], "service_account")
	}
	if got["id"] != sa.ShadowUserID.String() {
		t.Errorf("id: got %v, want %s (shadow user id)", got["id"], sa.ShadowUserID)
	}
	if got["tenant_id"] != env.tenantID.String() {
		t.Errorf("tenant_id: got %v, want %s", got["tenant_id"], env.tenantID)
	}
	if got["display_name"] != "ci-prod" {
		t.Errorf("display_name: got %v, want %q", got["display_name"], "ci-prod")
	}

	// email must be explicit JSON null — synthetic address must not be leaked.
	emailVal, emailPresent := got["email"]
	if !emailPresent {
		t.Error("email: field must be present (as null), not absent")
	}
	if emailVal != nil {
		t.Errorf("email: got %v, want null (synthetic SA email must not be exposed)", emailVal)
	}

	// Nested service_account object.
	saObj, ok := got["service_account"].(map[string]interface{})
	if !ok {
		t.Fatalf("service_account: expected object, got %T (%v)", got["service_account"], got["service_account"])
	}
	if saObj["id"] != sa.ID.String() {
		t.Errorf("service_account.id: got %v, want %s", saObj["id"], sa.ID)
	}
	if saObj["name"] != "ci-prod" {
		t.Errorf("service_account.name: got %v, want %q", saObj["name"], "ci-prod")
	}
	if saObj["description"] != "GitHub Actions deploy bot for myapp" {
		t.Errorf("service_account.description: got %v, want %q", saObj["description"], "GitHub Actions deploy bot for myapp")
	}
	// allowed_scopes should be ["pull","push"].
	rawScopes, _ := saObj["allowed_scopes"].([]interface{})
	if len(rawScopes) != 2 {
		t.Errorf("service_account.allowed_scopes: got %v, want [pull push]", rawScopes)
	} else {
		scopes := []string{rawScopes[0].(string), rawScopes[1].(string)}
		sort.Strings(scopes)
		if scopes[0] != "pull" || scopes[1] != "push" {
			t.Errorf("service_account.allowed_scopes: got %v, want [pull push]", scopes)
		}
	}

	// Human-only fields (user_id, username, created_at, memberships) must be absent
	// so the caller cannot infer shadow-user internal details.
	for _, f := range []string{"user_id", "username", "created_at", "memberships"} {
		if _, present := got[f]; present {
			t.Errorf("field %q must be absent for SA callers, was present", f)
		}
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
