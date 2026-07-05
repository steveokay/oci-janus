// Package handler — tests for the self-service TOTP MFA endpoints under
// /api/v1/users/me/mfa (Tier-1 #1). They reuse the in-memory fakes + miniredis
// harness from http_test.go, so no real Redis or PostgreSQL is required. A
// 32-byte MFA KEK is wired onto the test Service so the enrolment path (which
// encrypts the pending secret) works end-to-end.
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mfaTestKEK is a deterministic 32-byte AES-256 key-encryption key. It only
// encrypts the pending TOTP secret at rest inside the fake repo; it is never a
// real secret and never leaves the test process.
var mfaTestKEK = []byte("0123456789abcdef0123456789abcdef")

// newMFATestServer builds the standard handler test server and wires a valid
// 32-byte MFA KEK onto the Service so BeginMFAEnrollment can encrypt the secret.
func newMFATestServer(t *testing.T) (*httptest.Server, *testCtx) {
	t.Helper()
	srv, tc := newTestServer(t)
	tc.svc.SetMFAKEK(mfaTestKEK)
	return srv, tc
}

// ── GET /users/me/mfa ─────────────────────────────────────────────────────────

// TestMFAStatus_noAuth_returns401 verifies an unauthenticated status read is
// rejected with 401.
func TestMFAStatus_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/users/me/mfa")
	if err != nil {
		t.Fatalf("GET /users/me/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// TestMFAStatus_authed_returnsDisabled verifies a fresh (un-enrolled) user
// reports enabled=false.
func TestMFAStatus_authed_returnsDisabled(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-status", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me/mfa", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(buf))
	}
	var got struct {
		Enabled    bool    `json:"enabled"`
		EnrolledAt *string `json:"enrolled_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Enabled {
		t.Error("enabled: got true, want false for a fresh user")
	}
}

// ── POST /users/me/mfa/enroll ─────────────────────────────────────────────────

// TestMFAEnroll_authed_returnsSecretAndURI verifies a successful enrolment
// returns 200 with a non-empty base32 secret + otpauth URI. The KEK must be
// wired for the encrypt-at-rest step to succeed.
func TestMFAEnroll_authed_returnsSecretAndURI(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-enroll", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/mfa/enroll", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/mfa/enroll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200, body=%s", resp.StatusCode, string(buf))
	}
	var got struct {
		SecretBase32 string `json:"secret_base32"`
		OtpauthURI   string `json:"otpauth_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SecretBase32 == "" {
		t.Error("secret_base32: expected non-empty value")
	}
	if !strings.HasPrefix(got.OtpauthURI, "otpauth://") {
		t.Errorf("otpauth_uri: got %q, want an otpauth:// URI", got.OtpauthURI)
	}
}

// TestMFAEnroll_alreadyEnabled_returns409 verifies that enrolling when MFA is
// already on returns 409 with the MFAALREADYENABLED code — re-enrolment must be
// a conflict, not a silent secret rotation.
func TestMFAEnroll_alreadyEnabled_returns409(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-already", "Str0ng!Password123")
	// Seed the user as already-enrolled so BeginMFAEnrollment short-circuits.
	tc.users.seedMFAEnabled(userID)

	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/mfa/enroll", nil, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /users/me/mfa/enroll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 409, body=%s", resp.StatusCode, string(buf))
	}
	if code := firstErrorCode(t, resp.Body); code != "MFAALREADYENABLED" {
		t.Errorf("error code: got %q, want MFAALREADYENABLED", code)
	}
}

// ── POST /users/me/mfa/verify ─────────────────────────────────────────────────

// TestMFAVerify_badCode_returns400 verifies that after a pending secret exists
// (enroll first), a wrong TOTP code is rejected with 400 MFACODEINVALID rather
// than logging the user out.
func TestMFAVerify_badCode_returns400(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-verify", "Str0ng!Password123")

	// First enrol so a pending encrypted secret is stored — otherwise verify
	// would fail with "not enrolled" (500) rather than the invalid-code path.
	enrollReq := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/mfa/enroll", nil, userID, tenantID)
	enrollResp, err := http.DefaultClient.Do(enrollReq)
	if err != nil {
		t.Fatalf("POST enroll: %v", err)
	}
	_ = enrollResp.Body.Close()
	if enrollResp.StatusCode != http.StatusOK {
		t.Fatalf("enroll status: got %d, want 200", enrollResp.StatusCode)
	}

	// Now verify with a deliberately wrong 6-digit code.
	body, _ := json.Marshal(map[string]string{"code": "000000"})
	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/mfa/verify", body, userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 400, body=%s", resp.StatusCode, string(buf))
	}
	if code := firstErrorCode(t, resp.Body); code != "MFACODEINVALID" {
		t.Errorf("error code: got %q, want MFACODEINVALID", code)
	}
}

// TestMFAVerify_missingCode_returns400 verifies a body with no code is rejected
// before any service call.
func TestMFAVerify_missingCode_returns400(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-verify-empty", "Str0ng!Password123")

	req := makeMeRequest(t, srv, tc, http.MethodPost, "/api/v1/users/me/mfa/verify", []byte(`{}`), userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// ── DELETE /users/me/mfa ──────────────────────────────────────────────────────

// TestMFADisable_noReauth_returns401 verifies that disabling MFA without proving
// control (empty password + empty code) is rejected with 401 — the re-auth gate
// defends against a hijacked session silently removing the second factor.
func TestMFADisable_noReauth_returns401(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfa-disable", "Str0ng!Password123")
	tc.users.seedMFAEnabled(userID)

	// Empty body → no password, no code → reauth fails closed.
	req := makeMeRequest(t, srv, tc, http.MethodDelete, "/api/v1/users/me/mfa", []byte(`{}`), userID, tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /users/me/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 401, body=%s", resp.StatusCode, string(buf))
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// firstErrorCode decodes the standard {"errors":[{"code":...}]} envelope and
// returns the first error code, so tests can assert the machine-readable code.
func firstErrorCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var env struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if len(env.Errors) == 0 {
		t.Fatal("expected at least one error in the response envelope")
	}
	return env.Errors[0].Code
}
