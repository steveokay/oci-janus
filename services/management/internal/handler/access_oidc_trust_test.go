// Package handler — access_oidc_trust_test.go
//
// FUT-001 Task 13 — BFF tests for the 4 OIDC-trust admin routes.
//
// Each RPC is exercised with two cases:
//  1. Happy path with a tenant-admin caller (200 / 201 / 204).
//  2. Non-admin caller (writerToken) — expects 403 before any gRPC call.
//
// The tests share the same fakeAuthServer used across the handler package
// (defined in handler_test.go). We add stub methods for the 4 new OIDC-trust
// RPCs on that fake below. Tenant-admin token wiring (ValidateToken +
// GetUserPermissions) already lives in handler_test.go.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// ── ListOIDCTrusts ────────────────────────────────────────────────────

// TestListOIDCTrusts_tenantAdmin_returns200 asserts the happy path: a
// tenant-admin caller gets a wrapped {trusts: [...]} payload with the two
// canned rows from the fake auth server.
func TestListOIDCTrusts_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodGet, "/api/v1/access/oidc-trust", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.OIDCTrustListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := len(body.Trusts); got != 2 {
		t.Errorf("trust count: got %d, want 2", got)
	}
	// Sanity-check one wire field surfaces through.
	if body.Trusts[0].DisplayName == "" {
		t.Errorf("expected display_name to be populated in wire shape")
	}
}

// TestListOIDCTrusts_nonAdmin_returns403 asserts the RBAC gate rejects a
// writer-role caller with 403 before hitting the fake gRPC server.
func TestListOIDCTrusts_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/access/oidc-trust", nil)
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

// ── CreateOIDCTrust ───────────────────────────────────────────────────

// TestCreateOIDCTrust_tenantAdmin_returns201 asserts the happy path: a valid
// JSON body is forwarded to the auth service and the created trust is echoed
// back to the client.
func TestCreateOIDCTrust_tenantAdmin_returns201(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.CreateOIDCTrustRequestBody{
		ServiceAccountID:    "00000000-0000-0000-0000-000000000900",
		DisplayName:         "GitHub Actions main",
		IssuerURL:           "https://token.actions.githubusercontent.com",
		Audience:            "https://registry.example.com",
		SubjectPattern:      "repo:acme/api:ref:refs/heads/main",
		JWKSCacheTTLSeconds: 3600,
	})
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPost, "/api/v1/access/oidc-trust", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	var body handler.OIDCTrustResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID == "" {
		t.Errorf("expected non-empty id from create response")
	}
	if body.DisplayName != "GitHub Actions main" {
		t.Errorf("display_name: got %q, want echo from request", body.DisplayName)
	}
}

// TestCreateOIDCTrust_nonAdmin_returns403 asserts the RBAC gate rejects a
// non-admin before the JSON body is even parsed.
func TestCreateOIDCTrust_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.CreateOIDCTrustRequestBody{
		ServiceAccountID: "00000000-0000-0000-0000-000000000900",
		DisplayName:      "x",
		IssuerURL:        "https://token.actions.githubusercontent.com",
		Audience:         "https://registry.example.com",
		SubjectPattern:   "repo:acme/api:ref:refs/heads/main",
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/access/oidc-trust", bytes.NewReader(payload))
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

// ── UpdateOIDCTrust ───────────────────────────────────────────────────

// TestUpdateOIDCTrust_tenantAdmin_returns200 asserts a PATCH with mutable
// fields succeeds and the updated shape is returned.
func TestUpdateOIDCTrust_tenantAdmin_returns200(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.UpdateOIDCTrustRequestBody{
		DisplayName:         "GitHub Actions renamed",
		SubjectPattern:      "repo:acme/api:ref:refs/heads/release/*",
		JWKSCacheTTLSeconds: 7200,
	})
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodPatch,
		"/api/v1/access/oidc-trust/00000000-0000-0000-0000-0000000000aa", payload)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body handler.OIDCTrustResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DisplayName != "GitHub Actions renamed" {
		t.Errorf("display_name: got %q, want updated value", body.DisplayName)
	}
}

// TestUpdateOIDCTrust_nonAdmin_returns403 asserts the RBAC gate rejects a
// non-admin PATCH.
func TestUpdateOIDCTrust_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	payload, _ := json.Marshal(handler.UpdateOIDCTrustRequestBody{DisplayName: "x"})
	req, _ := http.NewRequest(http.MethodPatch,
		env.srv.URL+"/api/v1/access/oidc-trust/00000000-0000-0000-0000-0000000000aa",
		bytes.NewReader(payload))
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

// ── DeleteOIDCTrust ───────────────────────────────────────────────────

// TestDeleteOIDCTrust_tenantAdmin_returns204 asserts DELETE returns 204 with
// no body — the auth service returns google.protobuf.Empty and the BFF turns
// that into a bodyless 204.
func TestDeleteOIDCTrust_tenantAdmin_returns204(t *testing.T) {
	env := newTestEnv(t)
	req := newTenantAdminRequest(t, env.srv.URL, http.MethodDelete,
		"/api/v1/access/oidc-trust/00000000-0000-0000-0000-0000000000aa", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
}

// TestDeleteOIDCTrust_nonAdmin_returns403 asserts the RBAC gate rejects a
// non-admin DELETE.
func TestDeleteOIDCTrust_nonAdmin_returns403(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodDelete,
		env.srv.URL+"/api/v1/access/oidc-trust/00000000-0000-0000-0000-0000000000aa", nil)
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

// ── Fake server stubs ────────────────────────────────────────────────

// ListOIDCTrusts returns two canned trusts so the wire shape can be
// asserted end-to-end. The FE key-of interest (`trusts` at the top level) is
// what the FE hook binds to, so we verify the count survives the JSON hop.
func (s *fakeAuthServer) ListOIDCTrusts(_ context.Context, req *authv1.ListOIDCTrustsRequest) (*authv1.ListOIDCTrustsResponse, error) {
	now := timestamppb.Now()
	return &authv1.ListOIDCTrustsResponse{
		Trusts: []*authv1.OIDCTrust{
			{
				Id:                  "00000000-0000-0000-0000-0000000000a1",
				TenantId:            req.GetTenantId(),
				ServiceAccountId:    "00000000-0000-0000-0000-000000000900",
				DisplayName:         "GitHub Actions main",
				IssuerUrl:           "https://token.actions.githubusercontent.com",
				Audience:            "https://registry.example.com",
				SubjectPattern:      "repo:acme/api:ref:refs/heads/main",
				JwksCacheTtlSeconds: 3600,
				CreatedAt:           now,
				UpdatedAt:           now,
			},
			{
				Id:                  "00000000-0000-0000-0000-0000000000a2",
				TenantId:            req.GetTenantId(),
				ServiceAccountId:    "00000000-0000-0000-0000-000000000901",
				DisplayName:         "GitLab CI production",
				IssuerUrl:           "https://gitlab.example.com",
				Audience:            "https://registry.example.com",
				SubjectPattern:      "project_path:acme/api:ref_type:branch:ref:main",
				JwksCacheTtlSeconds: 3600,
				CreatedAt:           now,
				UpdatedAt:           now,
			},
		},
	}, nil
}

// CreateOIDCTrust echoes the input back with an assigned id. Real validation
// runs on the auth service; the BFF layer only needs to confirm the wire
// hop.
func (s *fakeAuthServer) CreateOIDCTrust(_ context.Context, req *authv1.CreateOIDCTrustRequest) (*authv1.OIDCTrust, error) {
	now := timestamppb.Now()
	return &authv1.OIDCTrust{
		Id:                  "00000000-0000-0000-0000-0000000000aa",
		TenantId:            req.GetTenantId(),
		ServiceAccountId:    req.GetServiceAccountId(),
		DisplayName:         req.GetDisplayName(),
		IssuerUrl:           req.GetIssuerUrl(),
		Audience:            req.GetAudience(),
		SubjectPattern:      req.GetSubjectPattern(),
		JwksCacheTtlSeconds: req.GetJwksCacheTtlSeconds(),
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

// UpdateOIDCTrust echoes the mutable fields back with the id from the path.
func (s *fakeAuthServer) UpdateOIDCTrust(_ context.Context, req *authv1.UpdateOIDCTrustRequest) (*authv1.OIDCTrust, error) {
	now := timestamppb.Now()
	return &authv1.OIDCTrust{
		Id:                  req.GetId(),
		TenantId:            req.GetTenantId(),
		DisplayName:         req.GetDisplayName(),
		SubjectPattern:      req.GetSubjectPattern(),
		JwksCacheTtlSeconds: req.GetJwksCacheTtlSeconds(),
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

// DeleteOIDCTrust returns Empty — the BFF turns that into a bodyless 204.
func (s *fakeAuthServer) DeleteOIDCTrust(_ context.Context, _ *authv1.DeleteOIDCTrustRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
