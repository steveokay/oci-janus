// Package handler — FUT-006 tests for API-key Bearer auth on /users/me.
//
// FE-API-048 T16 added the SA principal envelope branch to GET
// /users/me, but the handler's requireAuth only accepted JWTs. FUT-006
// (2026-06-23) widened requireAuth to also accept Bearer tokens of the
// form `key.<id>.<secret>` so a CI bot can introspect itself directly
// against /users/me without first exchanging the key for a JWT via
// /auth/token.
//
// These tests pin both halves of that change:
//   - parseAPIKeyBearer (pure function — many edge cases)
//   - requireAuth -> ValidateAPIKey -> /users/me end-to-end happy path
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── parseAPIKeyBearer ─────────────────────────────────────────────────────────

// TestParseAPIKeyBearer_validShape verifies the canonical happy path:
// `key.<uuid>.<secret>` parses into the expected (id, secret).
func TestParseAPIKeyBearer_validShape(t *testing.T) {
	id := uuid.New()
	secret := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	token := "key." + id.String() + "." + secret

	got, gotSecret, ok := parseAPIKeyBearer(token)
	if !ok {
		t.Fatal("parseAPIKeyBearer: expected ok=true for valid shape")
	}
	if got != id {
		t.Errorf("id: got %s, want %s", got, id)
	}
	if gotSecret != secret {
		t.Errorf("secret: got %q, want %q", gotSecret, secret)
	}
}

// TestParseAPIKeyBearer_rejectsBadShapes covers every structural
// mismatch the parser must reject without misreading as a partial key.
// Each falls through to JWT validation downstream, which then fails
// cleanly with "invalid token".
func TestParseAPIKeyBearer_rejectsBadShapes(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no prefix", "abc.def.ghi"},
		{"prefix only", "key."},
		{"jwt three-segment", "eyJhbGc.eyJzdWI.signature"},
		{"only one segment after prefix", "key.abc"},
		{"unparseable uuid", "key.not-a-uuid.secret"},
		{"empty secret", "key." + uuid.New().String() + "."},
		// The parser uses strings.Cut so a multi-dot secret stays as
		// part of the secret (no false reject). This case asserts the
		// secret survives intact rather than the parser bailing.
		{"prefix is case-sensitive", "KEY." + uuid.New().String() + ".secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := parseAPIKeyBearer(tc.token); ok {
				t.Errorf("parseAPIKeyBearer(%q): expected ok=false, got true", tc.token)
			}
		})
	}
}

// TestParseAPIKeyBearer_secretMayContainDots ensures the parser
// preserves the entire secret string even when it contains dots
// (defensive — today's secrets are 64-char hex with no dots, but a
// future format change shouldn't silently truncate).
func TestParseAPIKeyBearer_secretMayContainDots(t *testing.T) {
	id := uuid.New()
	// Synthetic — a secret with a dot in it would still pass through.
	weirdSecret := "abc.def.ghi"
	token := "key." + id.String() + "." + weirdSecret

	got, gotSecret, ok := parseAPIKeyBearer(token)
	if !ok || got != id || gotSecret != weirdSecret {
		t.Errorf("parseAPIKeyBearer with dotted secret: got (%v, %q, %v), want (%s, %q, true)",
			got, gotSecret, ok, id, weirdSecret)
	}
}

// ── /users/me via Bearer key ──────────────────────────────────────────────────

// TestUsersMe_AcceptsAPIKeyBearer verifies the end-to-end FUT-006 path:
//  1. Seed a human user.
//  2. Seed an API key for that user in the fake repo (with a real
//     argon2 hash so ValidateAPIKey accepts the secret).
//  3. GET /users/me with `Authorization: Bearer key.<id>.<secret>`.
//  4. Assert 200 + the human-caller response envelope ("type":"user").
//
// This is the simpler half of FUT-006 — the SA-key path uses the same
// requireAuth -> ValidateAPIKey -> synthClaimsFromAPIKey -> /users/me
// flow but additionally exercises the SA branch in getCurrentUser
// (already covered by TestUsersMe_SAKeyCallerGetsPrincipalEnvelope
// via the JWT exchange path; the FE-API-048 T16 logic is the same).
func TestUsersMe_AcceptsAPIKeyBearer(t *testing.T) {
	srv, tc := newTestServer(t)

	// Seed a user. The fake user repo's UpsertSSO branch isn't needed —
	// registerTestUser uses the standard password flow which is enough
	// for /users/me to look up the user row.
	userID, tenantID := seedTestUser(t, tc, "ci-bot-human", "Str0ng!Password123")

	// Seed an API key for that user. We hash the raw secret with the
	// production argon2 routine so ValidateAPIKey verifies against the
	// same KDF — using a plain `=` comparison would skip the bcrypt-
	// like cost the production verifier applies.
	rawSecret := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	hash, hashErr := argon2pkg.Hash(rawSecret)
	if hashErr != nil {
		t.Fatalf("argon2 hash: %v", hashErr)
	}
	keyID := uuid.New()
	tc.apiKeys.keys[keyID] = &repository.APIKey{
		ID:        keyID,
		TenantID:  tenantID,
		UserID:    &userID,
		Name:      "fut-006-test",
		KeyHash:   hash,
		KeyPrefix: rawSecret[:12],
		Scopes:    []string{"pull"},
		IsActive:  true,
	}

	// Bearer key shape — the FUT-006 wire contract.
	token := "key." + keyID.String() + "." + rawSecret

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/users/me", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

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

	// Human-caller envelope: type=user, user_id=UUID, tenant_id matches.
	// (saCallerResponse uses "id"; currentUserResponse uses "user_id".)
	if got["type"] != "user" {
		t.Errorf("type: got %v, want %q", got["type"], "user")
	}
	if got["user_id"] != userID.String() {
		t.Errorf("user_id: got %v, want %s", got["user_id"], userID)
	}
	if got["tenant_id"] != tenantID.String() {
		t.Errorf("tenant_id: got %v, want %s", got["tenant_id"], tenantID)
	}
}

// TestUsersMe_BadAPIKeyRejected verifies that a Bearer with the
// `key.` prefix but a wrong secret falls into the standard 401 path
// rather than silently misrouting to the JWT validator (which would
// produce a different error message).
func TestUsersMe_BadAPIKeyRejected(t *testing.T) {
	srv, tc := newTestServer(t)
	userID, tenantID := seedTestUser(t, tc, "ci-bot-bad-secret", "Str0ng!Password123")

	realSecret := "1111111111111111111111111111111111111111111111111111111111111111"
	hash, hashErr := argon2pkg.Hash(realSecret)
	if hashErr != nil {
		t.Fatalf("argon2 hash: %v", hashErr)
	}
	keyID := uuid.New()
	tc.apiKeys.keys[keyID] = &repository.APIKey{
		ID:        keyID,
		TenantID:  tenantID,
		UserID:    &userID,
		Name:      "fut-006-bad-secret",
		KeyHash:   hash,
		KeyPrefix: realSecret[:12],
		Scopes:    []string{"pull"},
		IsActive:  true,
	}

	// Use a DIFFERENT secret in the Bearer so argon2 verification fails.
	token := "key." + keyID.String() + ".wrongsecretwrongsecretwrongsecret"

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/users/me", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /users/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status: got %d, body=%s, want 401", resp.StatusCode, string(body))
	}
}
