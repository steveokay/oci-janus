// Package handler — tests for the two-step login HTTP surface: POST /login
// returning an MFA challenge for an MFA-enabled user, and POST /login/mfa
// exchanging that challenge + a TOTP code for an access token. They reuse the
// miniredis + in-memory fake harness from http_test.go. OTP codes and tokens
// are never logged.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/hotp"

	aespkg "github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// loginMFATestSecret is a valid base32 TOTP secret used to seed an MFA-enabled
// user directly (bypassing the enrol handshake) so the login test can compute a
// fresh, unused OTP. It is a fixed test constant, never a real secret.
const loginMFATestSecret = "JBSWY3DPEHPK3PXP"

// seedMFAEnabledWithSecret marks the user MFA-enabled with a known TOTP secret,
// encrypting it with the wired test KEK exactly as the real enrol path would.
// LastUsedCounter is left nil so a freshly-computed OTP is accepted (no replay).
func seedMFAEnabledWithSecret(t *testing.T, tc *testCtx, userID uuid.UUID, secretBase32 string) {
	t.Helper()
	enc, err := aespkg.Encrypt([]byte(secretBase32), mfaTestKEK)
	if err != nil {
		t.Fatalf("encrypt mfa secret: %v", err)
	}
	now := time.Now()
	tc.users.mfa[userID] = &repository.MFAState{Enabled: true, SecretEnc: enc, EnrolledAt: &now}
}

// totpNow returns the TOTP the service will accept for secretBase32 at the
// current wall clock — mirroring the service's counter derivation (unix / 30).
func totpNow(t *testing.T, secretBase32 string) string {
	t.Helper()
	counter := uint64(time.Now().Unix()) / 30 //nolint:gosec // test-only, bounded
	code, err := hotp.GenerateCode(secretBase32, counter)
	if err != nil {
		t.Fatalf("hotp.GenerateCode: %v", err)
	}
	return code
}

// TestLogin_mfaEnabled_returnsChallenge verifies POST /login for an MFA-enabled
// user returns 200 with {mfa_required:true, challenge_token:...} — not a token.
func TestLogin_mfaEnabled_returnsChallenge(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfalogin", "Str0ng!Password123")
	tc.users.seedMFAEnabled(userID)

	body, _ := json.Marshal(map[string]string{
		"tenant_id": tenantID.String(),
		"username":  "mfalogin",
		"password":  "Str0ng!Password123",
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200, body=%s", resp.StatusCode, string(buf))
	}
	var got struct {
		MFARequired    bool   `json:"mfa_required"`
		ChallengeToken string `json:"challenge_token"`
		Token          string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.MFARequired {
		t.Error("mfa_required: got false, want true")
	}
	if got.ChallengeToken == "" {
		t.Error("challenge_token: expected non-empty")
	}
	if got.Token != "" {
		t.Error("token: must be empty on an MFA-gated login")
	}
}

// TestLoginMFA_correctCode_returnsToken verifies POST /login/mfa with a valid
// challenge token + correct TOTP returns 200 with an access token.
func TestLoginMFA_correctCode_returnsToken(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfastep2", "Str0ng!Password123")
	seedMFAEnabledWithSecret(t, tc, userID, loginMFATestSecret)

	ct, err := tc.svc.IssueMFAChallengeToken(context.Background(), userID.String(), tenantID.String())
	if err != nil {
		t.Fatalf("IssueMFAChallengeToken: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"challenge_token": ct,
		"code":            totpNow(t, loginMFATestSecret),
	})
	resp, err := http.Post(srv.URL+"/api/v1/login/mfa", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200, body=%s", resp.StatusCode, string(buf))
	}
	var got struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Token == "" {
		t.Error("token: expected non-empty access token after MFA verify")
	}
}

// TestLoginMFA_wrongCode_returns401 verifies POST /login/mfa with a valid
// challenge token but a wrong code is rejected with 401.
func TestLoginMFA_wrongCode_returns401(t *testing.T) {
	srv, tc := newMFATestServer(t)
	userID, tenantID := seedTestUser(t, tc, "mfabad", "Str0ng!Password123")
	seedMFAEnabledWithSecret(t, tc, userID, loginMFATestSecret)

	ct, err := tc.svc.IssueMFAChallengeToken(context.Background(), userID.String(), tenantID.String())
	if err != nil {
		t.Fatalf("IssueMFAChallengeToken: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"challenge_token": ct, "code": "000000"})
	resp, err := http.Post(srv.URL+"/api/v1/login/mfa", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 401, body=%s", resp.StatusCode, string(buf))
	}
}

// TestLoginMFA_missingFields_returns400 verifies a body missing the challenge
// token or code is rejected before any service call.
func TestLoginMFA_missingFields_returns400(t *testing.T) {
	srv, _ := newMFATestServer(t)
	resp, err := http.Post(srv.URL+"/api/v1/login/mfa", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("POST /login/mfa: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}
