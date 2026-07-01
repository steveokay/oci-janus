// Package handler — access_oidc_trust.go
//
// FUT-001 Task 13 — BFF admin routes for federated workload identity.
//
// 4 routes, all authMW-gated and tenant-admin gated (matches the pattern
// established by tenant_users.go — reuse `isTenantAdminOrPlatformAdmin`):
//
//	GET    /api/v1/access/oidc-trust        → auth.ListOIDCTrusts
//	POST   /api/v1/access/oidc-trust        → auth.CreateOIDCTrust
//	PATCH  /api/v1/access/oidc-trust/{id}   → auth.UpdateOIDCTrust
//	DELETE /api/v1/access/oidc-trust/{id}   → auth.DeleteOIDCTrust
//
// Tenant id is taken from the JWT claims (middleware.TenantIDFromContext),
// NEVER from a request body field — defence against a caller sending a
// mismatched tenant_id in the JSON body. This is the isolation rule from
// CLAUDE.md §9.
//
// The auth service enforces every input validation (issuer-allowlist, glob
// syntax, audience non-empty, cross-tenant service-account rejection) and
// returns codes.InvalidArgument on failure. The BFF forwards those errors
// verbatim to the client via mapOIDCTrustGRPCError.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ── Wire shapes ───────────────────────────────────────────────────────

// OIDCTrustResponse is the JSON representation of one auth.v1.OIDCTrust row.
// Field names mirror the proto snake_case so the FE TanStack hook (see
// docs/superpowers/plans/2026-07-01-fut-001-federated-workload-identity.md
// Task 14) can decode directly without a rename step.
type OIDCTrustResponse struct {
	ID                  string     `json:"id"`
	TenantID            string     `json:"tenant_id"`
	ServiceAccountID    string     `json:"service_account_id"`
	DisplayName         string     `json:"display_name"`
	IssuerURL           string     `json:"issuer_url"`
	Audience            string     `json:"audience"`
	SubjectPattern      string     `json:"subject_pattern"`
	JWKSCacheTTLSeconds int32      `json:"jwks_cache_ttl_seconds"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	LastUsedAt          *time.Time `json:"last_used_at,omitempty"`
}

// OIDCTrustListResponse wraps the list under a `trusts` key so future
// pagination fields can be added without a breaking wire change.
type OIDCTrustListResponse struct {
	Trusts []OIDCTrustResponse `json:"trusts"`
}

// CreateOIDCTrustRequestBody is the JSON body accepted by POST. tenant_id
// is intentionally absent — it comes from the caller's JWT claims. The BFF
// silently ignores any tenant_id the caller submits (defence in depth vs.
// tenant-forgery attempts).
type CreateOIDCTrustRequestBody struct {
	ServiceAccountID    string `json:"service_account_id"`
	DisplayName         string `json:"display_name"`
	IssuerURL           string `json:"issuer_url"`
	Audience            string `json:"audience"`
	SubjectPattern      string `json:"subject_pattern"`
	JWKSCacheTTLSeconds int32  `json:"jwks_cache_ttl_seconds,omitempty"`
}

// UpdateOIDCTrustRequestBody is the JSON body accepted by PATCH. Only the
// three mutable fields (display_name, subject_pattern, jwks_cache_ttl_seconds)
// are exposed — issuer_url, audience, and service_account_id are append-only
// per the spec's "Delete+Create to change immutable fields" rule.
type UpdateOIDCTrustRequestBody struct {
	DisplayName         string `json:"display_name,omitempty"`
	SubjectPattern      string `json:"subject_pattern,omitempty"`
	JWKSCacheTTLSeconds int32  `json:"jwks_cache_ttl_seconds,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// handleListOIDCTrusts returns every trust row for the caller's tenant.
// Wraps the list in {trusts: [...]} for pagination-forward compatibility.
func (h *Handler) handleListOIDCTrusts(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.auth.ListOIDCTrusts(r.Context(), &authv1.ListOIDCTrustsRequest{
		TenantId: tenantID,
	})
	if err != nil {
		mapOIDCTrustGRPCError(w, "list OIDC trusts", err)
		return
	}

	trusts := make([]OIDCTrustResponse, 0, len(resp.GetTrusts()))
	for _, t := range resp.GetTrusts() {
		trusts = append(trusts, toOIDCTrustResponse(t))
	}
	writeJSON(w, http.StatusOK, OIDCTrustListResponse{Trusts: trusts})
}

// handleCreateOIDCTrust forwards the body to auth.CreateOIDCTrust after
// injecting the tenant id from the JWT claims. All input validation lives
// on the auth service; the BFF only enforces "tenant_id comes from the
// token, not the body" and returns whatever the auth service says.
func (h *Handler) handleCreateOIDCTrust(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body CreateOIDCTrustRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	trust, err := h.auth.CreateOIDCTrust(r.Context(), &authv1.CreateOIDCTrustRequest{
		TenantId:            tenantID,
		ServiceAccountId:    body.ServiceAccountID,
		DisplayName:         body.DisplayName,
		IssuerUrl:           body.IssuerURL,
		Audience:            body.Audience,
		SubjectPattern:      body.SubjectPattern,
		JwksCacheTtlSeconds: body.JWKSCacheTTLSeconds,
	})
	if err != nil {
		mapOIDCTrustGRPCError(w, "create OIDC trust", err)
		return
	}
	writeJSON(w, http.StatusCreated, toOIDCTrustResponse(trust))
}

// handleUpdateOIDCTrust forwards the mutable subset (display_name,
// subject_pattern, jwks_cache_ttl_seconds) to auth.UpdateOIDCTrust. The
// trust id is read from the URL path; the tenant id is read from the
// JWT claims so a caller can't PATCH another tenant's row even if they
// guess the id.
func (h *Handler) handleUpdateOIDCTrust(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body UpdateOIDCTrustRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	trust, err := h.auth.UpdateOIDCTrust(r.Context(), &authv1.UpdateOIDCTrustRequest{
		Id:                  id,
		TenantId:            tenantID,
		DisplayName:         body.DisplayName,
		SubjectPattern:      body.SubjectPattern,
		JwksCacheTtlSeconds: body.JWKSCacheTTLSeconds,
	})
	if err != nil {
		mapOIDCTrustGRPCError(w, "update OIDC trust", err)
		return
	}
	writeJSON(w, http.StatusOK, toOIDCTrustResponse(trust))
}

// handleDeleteOIDCTrust removes a trust row. Returns 204 with no body on
// success (the auth service returns google.protobuf.Empty). ON DELETE
// CASCADE from service_accounts is handled at the DB layer; this route is
// for the explicit-delete path only.
func (h *Handler) handleDeleteOIDCTrust(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	if _, err := h.auth.DeleteOIDCTrust(r.Context(), &authv1.DeleteOIDCTrustRequest{
		Id:       id,
		TenantId: tenantID,
	}); err != nil {
		mapOIDCTrustGRPCError(w, "delete OIDC trust", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────

// toOIDCTrustResponse converts the proto shape into the JSON wire shape.
// last_used_at is nil-safe: an OIDC trust that has never been exercised
// has a NULL last_used_at column in the DB, which surfaces here as nil
// on the pointer field so JSON emits nothing.
func toOIDCTrustResponse(t *authv1.OIDCTrust) OIDCTrustResponse {
	out := OIDCTrustResponse{
		ID:                  t.GetId(),
		TenantID:            t.GetTenantId(),
		ServiceAccountID:    t.GetServiceAccountId(),
		DisplayName:         t.GetDisplayName(),
		IssuerURL:           t.GetIssuerUrl(),
		Audience:            t.GetAudience(),
		SubjectPattern:      t.GetSubjectPattern(),
		JWKSCacheTTLSeconds: t.GetJwksCacheTtlSeconds(),
	}
	if ts := t.GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime()
	}
	if ts := t.GetUpdatedAt(); ts != nil {
		out.UpdatedAt = ts.AsTime()
	}
	if ts := t.GetLastUsedAt(); ts != nil {
		v := ts.AsTime()
		out.LastUsedAt = &v
	}
	return out
}

// mapOIDCTrustGRPCError translates the typed gRPC codes the auth service
// returns into HTTP statuses. Mirrors the shape used by mapTenantUserGRPCError
// (tenant_users.go) so operators debugging a failed request get the same
// language across surfaces.
//
// The auth service returns codes.InvalidArgument for every validation
// failure (issuer not allowlisted, subject_pattern glob syntax error,
// audience empty, cross-tenant service_account_id, etc.) — those all
// surface as 400 with the auth service's original message.
func mapOIDCTrustGRPCError(w http.ResponseWriter, op string, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, st.Message())
			return
		case codes.AlreadyExists:
			writeError(w, http.StatusConflict, st.Message())
			return
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "OIDC trust not found")
			return
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, st.Message())
			return
		case codes.FailedPrecondition:
			writeError(w, http.StatusPreconditionFailed, st.Message())
			return
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(w, http.StatusServiceUnavailable, "auth service unavailable")
		return
	}
	slog.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "failed to "+op)
}
