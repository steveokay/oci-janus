// Package handler — access_review_test.go
//
// FUT-004 Task 8 — BFF tests for the 2 access-review routes.
//
// Cases:
//   - admin list happy-path        → 200, full list.
//   - non-admin list                → 200, only keys owned by caller.
//   - admin snooze happy-path       → 200, days plumbed to auth service.
//   - admin snooze out-of-range     → 400 (BFF revalidates before RPC).
//   - non-admin snoozes own key     → 200 (owner-of-key branch).
//   - non-admin snoozes foreign key → 403 (or 404 for unknown id).
//
// Shares fakeAuthServer + tenant-admin token wiring from handler_test.go +
// tenant_users_test.go. Stubs for ListStaleKeys / SnoozeAPIKeyReview are
// appended at the bottom of this file.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// staleKeyIDAdminOwned belongs to tenant-admin-user; used to prove the
// admin-list path returns keys regardless of owner.
const staleKeyIDAdminOwned = "11111111-1111-1111-1111-111111111111"

// staleKeyIDWriterOwned belongs to writer-user; used to prove the
// non-admin-list path returns only own keys.
const staleKeyIDWriterOwned = "22222222-2222-2222-2222-222222222222"

// staleKeyIDOtherOwned belongs to some third user; used to prove the
// non-admin-snooze path rejects foreign-key snoozes with 403.
const staleKeyIDOtherOwned = "33333333-3333-3333-3333-333333333333"

// ── ListStaleKeys ─────────────────────────────────────────────────────

// TestListStaleKeys_admin_returnsAll asserts the admin path bypasses the
// owner filter — a tenant-admin caller sees every stale key in the tenant.
func TestListStaleKeys_admin_returnsAll(t *testing.T) {
	env := newTestEnv(t)
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodGet, "/api/v1/access/review/stale", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.ListStaleKeysResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Fake seeds 3 keys — admin should see them all.
	if len(body.Keys) != 3 {
		t.Fatalf("keys: got %d, want 3 (admin sees all)", len(body.Keys))
	}
	// Suggested-action stringifier must strip the SUGGESTED_ACTION_ prefix
	// — the FE type union expects the short form.
	sawRevoke := false
	for _, k := range body.Keys {
		if k.SuggestedAction == "REVOKE" {
			sawRevoke = true
		}
	}
	if !sawRevoke {
		t.Errorf("expected at least one REVOKE-suggested key in admin list")
	}
}

// TestListStaleKeys_nonAdmin_filtersToOwn asserts a writer-role caller
// only sees keys where owner_user_id == caller.sub. The fake seeds one
// key with owner_user_id="writer-user" specifically for this case.
func TestListStaleKeys_nonAdmin_filtersToOwn(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/access/review/stale", nil)
	req.Header.Set("Authorization", "Bearer "+writerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (non-admin gets filtered list, not 403)", resp.StatusCode)
	}
	var body handler.ListStaleKeysResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Keys) != 1 {
		t.Fatalf("keys: got %d, want 1 (only writer-owned key)", len(body.Keys))
	}
	if body.Keys[0].OwnerUserID != "writer-user" {
		t.Errorf("owner: got %q, want %q", body.Keys[0].OwnerUserID, "writer-user")
	}
	if body.Keys[0].ID != staleKeyIDWriterOwned {
		t.Errorf("id: got %q, want %q", body.Keys[0].ID, staleKeyIDWriterOwned)
	}
}

// ── SnoozeAPIKeyReview ────────────────────────────────────────────────

// TestSnoozeAPIKeyReview_admin_happyPath asserts a tenant-admin can snooze
// any key. The fake echoes actor_id back as the owner on the returned row
// so we can also assert the JWT sub was plumbed through — mirroring the
// FUT-003 token-policy test.
func TestSnoozeAPIKeyReview_admin_happyPath(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.SnoozeAPIKeyReviewRequestBody{
		KeyID: staleKeyIDOtherOwned, // some other user's key
		Days:  30,
	})
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPost, "/api/v1/access/review/snooze", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.StaleKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != staleKeyIDOtherOwned {
		t.Errorf("id: got %q, want %q", body.ID, staleKeyIDOtherOwned)
	}
	if body.ReviewSnoozedUntil == nil {
		t.Errorf("review_snoozed_until: got nil, want non-nil (server echoed the snooze)")
	}
	// The fake echoes the actor_id as owner_user_id so we can prove the
	// BFF plumbed the JWT sub into SnoozeAPIKeyReviewRequest.ActorID
	// rather than accepting it from the request body.
	if body.OwnerUserID != "tenant-admin-user" {
		t.Errorf("owner (echoed actor_id): got %q, want %q (JWT sub)",
			body.OwnerUserID, "tenant-admin-user")
	}
}

// TestSnoozeAPIKeyReview_daysOutOfRange_returns400 asserts the BFF
// short-circuits with a 400 before the RPC when days is outside [1, 90].
// This is defence in depth on top of the auth service's own validation.
func TestSnoozeAPIKeyReview_daysOutOfRange_returns400(t *testing.T) {
	cases := []int32{0, -1, 91, 365}
	for _, days := range cases {
		days := days
		t.Run(daysLabel(days), func(t *testing.T) {
			env := newTestEnv(t)
			payload, _ := json.Marshal(handler.SnoozeAPIKeyReviewRequestBody{
				KeyID: staleKeyIDAdminOwned,
				Days:  days,
			})
			req := newTenantAdminRequest(t, env.srv.URL, http.MethodPost,
				"/api/v1/access/review/snooze", payload)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400 for days=%d", resp.StatusCode, days)
			}
		})
	}
}

// TestSnoozeAPIKeyReview_nonAdmin_ownKey_returns200 asserts the owner-of-key
// branch: a writer-role caller can snooze a key they own without needing
// tenant-admin. Exercises the pre-RPC ownership resolution via ListStaleKeys.
func TestSnoozeAPIKeyReview_nonAdmin_ownKey_returns200(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.SnoozeAPIKeyReviewRequestBody{
		KeyID: staleKeyIDWriterOwned,
		Days:  30,
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/access/review/snooze",
		bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+writerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (owner can snooze own key)", resp.StatusCode)
	}
}

// TestSnoozeAPIKeyReview_nonAdmin_foreignKey_returns403 asserts a
// writer-role caller trying to snooze another user's key gets 403 (or
// 404 for a key that isn't in the tenant's stale list at all).
func TestSnoozeAPIKeyReview_nonAdmin_foreignKey_returns403(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.SnoozeAPIKeyReviewRequestBody{
		KeyID: staleKeyIDOtherOwned, // owned by "other-user"
		Days:  30,
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/access/review/snooze",
		bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+writerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (writer cannot snooze foreign key)", resp.StatusCode)
	}
}

// TestSnoozeAPIKeyReview_missingKeyID_returns400 asserts an empty body
// key_id fails fast at the BFF rather than pushing an obviously bad UUID
// into the auth service just to bounce back as InvalidArgument.
func TestSnoozeAPIKeyReview_missingKeyID_returns400(t *testing.T) {
	env := newTestEnv(t)
	payload := []byte(`{"days":30}`)
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPost,
		"/api/v1/access/review/snooze", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for missing key_id", resp.StatusCode)
	}
}

// daysLabel gives each sub-test a legible name — "days=-1" reads better
// in `go test -v` output than the raw integer.
func daysLabel(d int32) string {
	switch {
	case d == 0:
		return "days=0"
	case d < 0:
		return "days_negative"
	default:
		return "days_over_max"
	}
}

// ── Fake server stubs ────────────────────────────────────────────────

// ListStaleKeys returns three canned keys — one per test owner — so the
// admin path can assert "sees all" and the non-admin path can assert
// "sees only own". The RotationDueAt / LastUsedAt on each row exists so
// the JSON wire shape's nullable-timestamp handling is exercised too.
func (s *fakeAuthServer) ListStaleKeys(_ context.Context, req *authv1.ListStaleKeysRequest) (*authv1.ListStaleKeysResponse, error) {
	// Guard against a caller passing a bogus tenant — the fake doesn't
	// parse UUIDs, but we do return an empty list for a mismatched tenant
	// so a copy-paste test bug surfaces as "got 0 want N" rather than a
	// silent pass.
	if req.GetTenantId() != testTenantID {
		return &authv1.ListStaleKeysResponse{}, nil
	}
	now := timestamppb.Now()
	return &authv1.ListStaleKeysResponse{
		Keys: []*authv1.StaleKey{
			{
				Id:              staleKeyIDAdminOwned,
				TenantId:        testTenantID,
				OwnerUserId:     "tenant-admin-user",
				Name:            "admin-ci-bot",
				LastUsedAt:      now,
				SuggestedAction: authv1.SuggestedAction_SUGGESTED_ACTION_KEEP,
				Reason:          "idle",
			},
			{
				Id:              staleKeyIDWriterOwned,
				TenantId:        testTenantID,
				OwnerUserId:     "writer-user",
				Name:            "writer-laptop-key",
				LastUsedAt:      nil, // never used → REVOKE-worthy
				SuggestedAction: authv1.SuggestedAction_SUGGESTED_ACTION_REVOKE,
				Reason:          "idle",
			},
			{
				Id:              staleKeyIDOtherOwned,
				TenantId:        testTenantID,
				OwnerUserId:     "other-user",
				Name:            "someone-elses-key",
				LastUsedAt:      now,
				SuggestedAction: authv1.SuggestedAction_SUGGESTED_ACTION_SNOOZE,
				Reason:          "idle",
			},
		},
	}, nil
}

// SnoozeAPIKeyReview echoes the request back as a StaleKey with the
// actor_id planted in owner_user_id so the test can assert the BFF
// plumbed the JWT sub into ActorID (and NOT from the request body).
// review_snoozed_until is set to a canned future timestamp so the FE
// wire-shape's *time.Time handling is exercised.
func (s *fakeAuthServer) SnoozeAPIKeyReview(_ context.Context, req *authv1.SnoozeAPIKeyReviewRequest) (*authv1.StaleKey, error) {
	if req.GetActorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "actor_id is required")
	}
	return &authv1.StaleKey{
		Id:                 req.GetKeyId(),
		TenantId:           testTenantID,
		OwnerUserId:        req.GetActorId(), // echoed for assertion
		ReviewSnoozedUntil: timestamppb.Now(),
	}, nil
}
