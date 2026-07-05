// Package handler — access_token_policy_test.go
//
// FUT-003 Task 10 — BFF tests for the 2 token-policy admin routes.
//
// Each RPC is exercised with two cases:
//  1. Happy path with a tenant-admin caller (200).
//  2. Non-admin caller (writerToken) — expects 403 before any gRPC call.
//
// Shares the fakeAuthServer used across the handler package (defined in
// handler_test.go). Stub methods for GetTokenPolicy / PutTokenPolicy are
// appended below. Tenant-admin token wiring (ValidateToken +
// GetUserPermissions) already lives in handler_test.go.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ── GetTokenPolicy ────────────────────────────────────────────────────

// TestGetTokenPolicy_tenantAdmin_returns200 asserts the happy path: a
// tenant-admin caller gets the current TokenPolicy for their tenant.
// The wire shape must expose the three limit fields as nullable *int32 so an
// unset (never-configured) tenant returns explicit nulls.
func TestGetTokenPolicy_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodGet, "/api/v1/access/token-policy", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.TokenPolicyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TenantID != testTenantID {
		t.Errorf("tenant_id: got %q, want %q", body.TenantID, testTenantID)
	}
	if body.MaxTTLDays == nil || *body.MaxTTLDays != 90 {
		t.Errorf("max_ttl_days: got %v, want 90", body.MaxTTLDays)
	}
	if body.RotationIntervalDays == nil || *body.RotationIntervalDays != 30 {
		t.Errorf("rotation_interval_days: got %v, want 30", body.RotationIntervalDays)
	}
	if body.IdleRevokeDays == nil || *body.IdleRevokeDays != 60 {
		t.Errorf("idle_revoke_days: got %v, want 60", body.IdleRevokeDays)
	}
}

// TestGetTokenPolicy_nonAdmin_returns403 asserts the RBAC gate rejects a
// writer-role caller before hitting the fake gRPC server.
func TestGetTokenPolicy_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/access/token-policy", nil)
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

// ── PutTokenPolicy ────────────────────────────────────────────────────

// TestPutTokenPolicy_tenantAdmin_returns200 asserts a PUT with a partial JSON
// body (only two of three limits set) is forwarded to the auth service with
// the missing field as nil (preserve-existing) and the updated TokenPolicy is
// echoed back.
func TestPutTokenPolicy_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	maxTTL := int32(30)
	idle := int32(14)
	requireMFA := true
	payload, _ := json.Marshal(handler.PutTokenPolicyRequestBody{
		MaxTTLDays:     &maxTTL,
		IdleRevokeDays: &idle,
		RequireMFA:     &requireMFA,
		// RotationIntervalDays intentionally omitted — preserve existing.
	})
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPut, "/api/v1/access/token-policy", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.TokenPolicyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxTTLDays == nil || *body.MaxTTLDays != 30 {
		t.Errorf("max_ttl_days: got %v, want 30 (echoed from request)", body.MaxTTLDays)
	}
	if body.IdleRevokeDays == nil || *body.IdleRevokeDays != 14 {
		t.Errorf("idle_revoke_days: got %v, want 14 (echoed from request)", body.IdleRevokeDays)
	}
	// require_mfa round-trips through the BFF → gRPC → BFF mapping (Task 14).
	if !body.RequireMFA {
		t.Errorf("require_mfa: got false, want true (echoed from request)")
	}
	// UpdatedByUserID should be the tenant-admin caller's user id — proves
	// actor_id was plumbed from the JWT sub, not from the request body.
	if body.UpdatedByUserID != "tenant-admin-user" {
		t.Errorf("updated_by_user_id: got %q, want %q (from JWT sub)",
			body.UpdatedByUserID, "tenant-admin-user")
	}
}

// TestPutTokenPolicy_omitRequireMFA_preservesExisting asserts the preserve-on-nil
// fix: a partial PUT that omits require_mfa (e.g. a CLI/Terraform client updating
// only a TTL limit) must NOT clobber an enabled MFA policy to false. The BFF reads
// the current policy (require_mfa=true in the fake) and forwards that value.
func TestPutTokenPolicy_omitRequireMFA_preservesExisting(t *testing.T) {
	env := newTestEnv(t)
	maxTTL := int32(45)
	// Body omits require_mfa entirely.
	payload, _ := json.Marshal(handler.PutTokenPolicyRequestBody{MaxTTLDays: &maxTTL})
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPut, "/api/v1/access/token-policy", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.TokenPolicyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.RequireMFA {
		t.Error("require_mfa: got false, want true preserved from the existing policy on a partial PUT")
	}
}

// TestPutTokenPolicy_nonAdmin_returns403 asserts the RBAC gate rejects a
// non-admin PUT before the JSON body is even parsed.
func TestPutTokenPolicy_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	maxTTL := int32(30)
	payload, _ := json.Marshal(handler.PutTokenPolicyRequestBody{MaxTTLDays: &maxTTL})
	req, _ := http.NewRequest(http.MethodPut, env.srv.URL+"/api/v1/access/token-policy", bytes.NewReader(payload))
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

// ── Fake server stubs ────────────────────────────────────────────────

// GetTokenPolicy returns a canned policy row so the wire shape (including the
// three nullable limit fields) can be asserted end-to-end.
func (s *fakeAuthServer) GetTokenPolicy(_ context.Context, req *authv1.GetTokenPolicyRequest) (*authv1.TokenPolicy, error) {
	return &authv1.TokenPolicy{
		TenantId:             req.GetTenantId(),
		MaxTtlDays:           wrapperspb.Int32(90),
		RotationIntervalDays: wrapperspb.Int32(30),
		IdleRevokeDays:       wrapperspb.Int32(60),
		// Canned require_mfa=true so the preserve-on-nil PUT test can assert the
		// BFF read this value back rather than clobbering it to false.
		RequireMfa:      true,
		UpdatedAt:       timestamppb.Now(),
		UpdatedByUserId: "seed-admin-user",
	}, nil
}

// PutTokenPolicy echoes the input back with updated_by_user_id set from the
// actor_id on the request — that is how the test asserts the BFF plumbed the
// JWT sub into ActorID (and not, say, from the request body).
func (s *fakeAuthServer) PutTokenPolicy(_ context.Context, req *authv1.PutTokenPolicyRequest) (*authv1.TokenPolicy, error) {
	return &authv1.TokenPolicy{
		TenantId:             req.GetTenantId(),
		MaxTtlDays:           req.GetMaxTtlDays(),
		RotationIntervalDays: req.GetRotationIntervalDays(),
		IdleRevokeDays:       req.GetIdleRevokeDays(),
		RequireMfa:           req.GetRequireMfa(),
		UpdatedAt:            timestamppb.Now(),
		UpdatedByUserId:      req.GetActorId(),
	}, nil
}
