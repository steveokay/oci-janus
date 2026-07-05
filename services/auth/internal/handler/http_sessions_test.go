// Package handler — tests for the self-service session-list endpoints under
// /api/v1/users/me/sessions (Tier-1 #1). They reuse the in-memory fakes +
// miniredis harness from http_test.go, so no real Redis or PostgreSQL is
// required. The Service's session repo is not wired by newTestServer, so each
// test wires an in-memory fake via tc.svc.SetSessionRepo before driving the
// handlers (mirrors how newMFATestServer wires the MFA KEK).
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── in-memory session repo fake ───────────────────────────────────────────────

// handlerFakeSessionRepo is an in-memory sessionRepo for the handler tests. The
// service-package fake of the same shape is unexported and unusable here, so we
// re-declare it locally. Rows are keyed by sid string. The signatures match the
// service's sessionRepo interface exactly (Create/ListLive/RevokeOwned/
// RevokeOthers/TouchLastActive) so SetSessionRepo accepts it.
type handlerFakeSessionRepo struct {
	bySID map[string]*repository.Session
}

// newHandlerFakeSessionRepo builds an empty in-memory session repo.
func newHandlerFakeSessionRepo() *handlerFakeSessionRepo {
	return &handlerFakeSessionRepo{bySID: map[string]*repository.Session{}}
}

// Create stores a copy of the row so callers can't mutate it out from under us.
func (f *handlerFakeSessionRepo) Create(_ context.Context, s repository.Session) error {
	c := s
	f.bySID[s.SID.String()] = &c
	return nil
}

// ListLive returns the user's non-revoked sessions (idle cutoff is ignored by
// the fake — seeded rows are always considered live).
func (f *handlerFakeSessionRepo) ListLive(_ context.Context, userID uuid.UUID, _ time.Time) ([]repository.Session, error) {
	var out []repository.Session
	for _, s := range f.bySID {
		if s.UserID == userID && s.RevokedAt == nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

// RevokeOwned revokes a single session iff it exists, belongs to userID, and is
// still live. ok=false covers absent / not-owned / already-revoked so the
// handler cannot distinguish "not yours" from "gone" (no ownership leak).
func (f *handlerFakeSessionRepo) RevokeOwned(_ context.Context, userID, sid uuid.UUID) (time.Time, bool, error) {
	s, ok := f.bySID[sid.String()]
	if !ok || s.UserID != userID || s.RevokedAt != nil {
		return time.Time{}, false, nil
	}
	now := time.Now()
	s.RevokedAt = &now
	return s.ExpiresAt, true, nil
}

// RevokeOthers revokes every live session for userID except keepSID and returns
// the rows it revoked.
func (f *handlerFakeSessionRepo) RevokeOthers(_ context.Context, userID, keepSID uuid.UUID) ([]repository.Session, error) {
	var out []repository.Session
	for _, s := range f.bySID {
		if s.UserID == userID && s.SID != keepSID && s.RevokedAt == nil {
			now := time.Now()
			s.RevokedAt = &now
			out = append(out, *s)
		}
	}
	return out, nil
}

// TouchLastActive is a no-op in the fake (last-active telemetry is not asserted).
func (f *handlerFakeSessionRepo) TouchLastActive(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// seedSession inserts a live session row (expires in the future, last-active
// now) for the given user/tenant into the fake and returns its freshly minted
// sid so the caller can flag/revoke it.
func seedSession(t *testing.T, fake *handlerFakeSessionRepo, userID, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	now := time.Now()
	if err := fake.Create(context.Background(), repository.Session{
		SID:          sid,
		UserID:       userID,
		TenantID:     tenantID,
		DeviceLabel:  "test-device",
		UserAgent:    "test-agent/1.0",
		IP:           "203.0.113.7",
		CreatedAt:    now,
		LastActiveAt: now,
		ExpiresAt:    now.Add(24 * time.Hour), // live
	}); err != nil {
		t.Fatalf("seedSession Create: %v", err)
	}
	return sid
}

// makeMeSessionRequest builds an authenticated request against the session
// endpoints. Unlike makeMeRequest it mints the bearer token with a sid claim
// (via IssueToken's trailing sid arg) so the token is tied to the caller's
// current session — that's what the handler compares against for the `current`
// flag and what revoke-others keeps.
func makeMeSessionRequest(t *testing.T, srv *httptest.Server, tc *testCtx, method, path string, body []byte, userID, tenantID, currentSID uuid.UUID) *http.Request {
	t.Helper()
	tok, err := tc.svc.IssueToken(context.Background(), userID.String(), tenantID.String(),
		nil, nil, false, "human", []string{"pwd"}, currentSID.String())
	if err != nil {
		t.Fatalf("IssueToken (with sid): %v", err)
	}
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

// ── GET /users/me/sessions + DELETE /users/me/sessions/{sid} ───────────────────

// TestSessions_listAndRevoke covers the happy path: a user with two live
// sessions lists them (current one flagged), then revokes the other and gets 204.
func TestSessions_listAndRevoke(t *testing.T) {
	srv, tc := newMFATestServer(t)
	fake := newHandlerFakeSessionRepo()
	tc.svc.SetSessionRepo(fake)
	userID, tenantID := seedTestUser(t, tc, "sess-user", "Str0ng!Password123")
	current := seedSession(t, fake, userID, tenantID)
	other := seedSession(t, fake, userID, tenantID)

	// List -> 2 sessions, current flagged.
	req := makeMeSessionRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me/sessions", nil, userID, tenantID, current)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET sessions: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	var got struct {
		Sessions []struct {
			Sid     string `json:"sid"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got.Sessions))
	}
	var sawCurrent bool
	for _, s := range got.Sessions {
		if s.Sid == current.String() && s.Current {
			sawCurrent = true
		}
	}
	if !sawCurrent {
		t.Fatal("current session must be flagged")
	}

	// Revoke the other -> 204.
	del := makeMeSessionRequest(t, srv, tc, http.MethodDelete, "/api/v1/users/me/sessions/"+other.String(), nil, userID, tenantID, current)
	dresp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatalf("DELETE session: %v", err)
	}
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status %d, want 204", dresp.StatusCode)
	}
	dresp.Body.Close()
}

// TestSessions_crossUser_404 pins the ownership boundary: an attacker cannot
// revoke another user's session — the repo scopes RevokeOwned by user_id, so
// the handler returns 404 (never a cross-user revoke, never a leak of the sid's
// existence).
func TestSessions_crossUser_404(t *testing.T) {
	srv, tc := newMFATestServer(t)
	fake := newHandlerFakeSessionRepo()
	tc.svc.SetSessionRepo(fake)
	userID, tenantID := seedTestUser(t, tc, "sess-owner", "Str0ng!Password123")
	attackerID, attackerTenant := seedTestUser(t, tc, "sess-attacker", "Str0ng!Password123")
	victimSID := seedSession(t, fake, userID, tenantID)

	// Attacker tries to revoke the victim's session -> 404 (ownership scoped).
	del := makeMeSessionRequest(t, srv, tc, http.MethodDelete, "/api/v1/users/me/sessions/"+victimSID.String(), nil, attackerID, attackerTenant, uuid.Nil)
	resp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatalf("DELETE session (attacker): %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user revoke status %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// The victim's session must remain live — nothing was torn down.
	if s := fake.bySID[victimSID.String()]; s == nil || s.RevokedAt != nil {
		t.Error("victim session must remain live after a rejected cross-user revoke")
	}
}
