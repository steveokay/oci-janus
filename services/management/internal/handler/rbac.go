// Package handler — RBAC management endpoints.
//
// These handlers translate REST calls into GrantRole / RevokeRole / ListMembers
// gRPC calls against registry-auth. Role enforcement is applied to the existing
// destructive routes (delete repo, delete tag, create repo) using the helpers
// defined here.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// roleHierarchy defines the ordering of roles from least to most privileged.
// Index 0 = least privileged.
var roleHierarchy = []string{"reader", "writer", "admin", "owner"}

// roleIndex returns the numeric rank of a role name in the hierarchy.
// Unknown roles return -1 (below all defined roles).
func roleIndex(role string) int {
	for i, r := range roleHierarchy {
		if r == role {
			return i
		}
	}
	return -1
}

// hasRole returns true if any entry in roles meets or exceeds the minimum role level.
// Used for UX-layer enforcement; the gRPC layer enforces independently.
func hasRole(roles []string, minimum string) bool {
	minIdx := roleIndex(minimum)
	if minIdx < 0 {
		return false
	}
	for _, r := range roles {
		if roleIndex(r) >= minIdx {
			return true
		}
	}
	return false
}

// getUserRoles calls GetUserPermissions on the auth gRPC client and returns the
// caller's role names for the current tenant. Returns an empty slice on error
// rather than propagating — callers that need enforcement must check the returned
// bool from hasRole rather than trusting an empty list.
func (h *Handler) getUserRoles(r *http.Request) []string {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	resp, err := h.auth.GetUserPermissions(r.Context(), &authv1.GetUserPermissionsRequest{
		UserId:   userID,
		TenantId: tenantID,
	})
	if err != nil {
		slog.Warn("GetUserPermissions failed", "err", err)
		return nil
	}
	return resp.GetRoles()
}

// authClient returns the auth client as AuthServiceClientWithRBAC.
// The cast is safe because New() receives the connection and constructs an
// extended client — see the management cmd/server/main.go wiring.
func (h *Handler) rbacClient() authv1.AuthServiceClientWithRBAC {
	c, ok := h.auth.(authv1.AuthServiceClientWithRBAC)
	if !ok {
		// Fallback: wrap in the extended client using the underlying connection.
		// This should never happen in production since main.go uses
		// NewAuthServiceClientWithRBAC.
		return nil
	}
	return c
}

// ---------------------------------------------------------------------------
// Route registration for RBAC endpoints (called from Register)
// ---------------------------------------------------------------------------

// RegisterRBAC mounts RBAC management routes onto mux under the authMW middleware.
// Called from Handler.Register to keep the main file clean.
func (h *Handler) RegisterRBAC(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	// Org membership
	mux.Handle("GET /api/v1/orgs/{org}/members",
		authMW(http.HandlerFunc(h.handleListOrgMembers)))
	mux.Handle("POST /api/v1/orgs/{org}/members",
		authMW(http.HandlerFunc(h.handleGrantOrgMember)))
	mux.Handle("DELETE /api/v1/orgs/{org}/members/{assignmentID}",
		authMW(http.HandlerFunc(h.handleRevokeOrgMember)))

	// Repo membership
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/members",
		authMW(http.HandlerFunc(h.handleListRepoMembers)))
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/members",
		authMW(http.HandlerFunc(h.handleGrantRepoMember)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/members/{assignmentID}",
		authMW(http.HandlerFunc(h.handleRevokeRepoMember)))
}

// ---------------------------------------------------------------------------
// Org member handlers
// ---------------------------------------------------------------------------

// MemberResponse is the JSON representation of a single role assignment.
type MemberResponse struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
	GrantedBy  string `json:"granted_by"`
}

// handleListOrgMembers returns all role assignments for the given org scope.
func (h *Handler) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	resp, err := rc.ListMembers(r.Context(), &authv1.ListMembersRequest{
		TenantId:   tenantID,
		ScopeType:  "org",
		ScopeValue: org,
	})
	if err != nil {
		slog.Error("ListMembers", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	members := memberSlice(resp.GetMembers())
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// grantMemberBody is the JSON body for POST …/members.
type grantMemberBody struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// handleGrantOrgMember assigns a role to a user for the given org.
func (h *Handler) handleGrantOrgMember(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	// Only admin or owner may grant roles.
	roles := h.getUserRoles(r)
	if !hasRole(roles, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body grantMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.UserID == "" || body.Role == "" {
		writeError(w, http.StatusBadRequest, "user_id and role are required")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	if _, err := rc.GrantRole(r.Context(), &authv1.GrantRoleRequest{
		TenantId:   tenantID,
		UserId:     body.UserID,
		Role:       body.Role,
		ScopeType:  "org",
		ScopeValue: org,
		GrantedBy:  callerID,
	}); err != nil {
		slog.Error("GrantRole", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to grant role")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeOrgMember removes a specific org role assignment.
func (h *Handler) handleRevokeOrgMember(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")
	assignmentID := r.PathValue("assignmentID")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	// Only admin or owner may revoke roles.
	roles := h.getUserRoles(r)
	if !hasRole(roles, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	if _, err := rc.RevokeRole(r.Context(), &authv1.RevokeRoleRequest{
		TenantId:     tenantID,
		AssignmentId: assignmentID,
	}); err != nil {
		slog.Error("RevokeRole", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke role")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Repo member handlers
// ---------------------------------------------------------------------------

// handleListRepoMembers returns all role assignments for the given repo scope.
func (h *Handler) handleListRepoMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	resp, err := rc.ListMembers(r.Context(), &authv1.ListMembersRequest{
		TenantId:   tenantID,
		ScopeType:  "repo",
		ScopeValue: org + "/" + repoName,
	})
	if err != nil {
		slog.Error("ListMembers (repo)", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	members := memberSlice(resp.GetMembers())
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

// handleGrantRepoMember assigns a role to a user for the given repo.
func (h *Handler) handleGrantRepoMember(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Writer or above may grant repo-level roles.
	roles := h.getUserRoles(r)
	if !hasRole(roles, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body grantMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.UserID == "" || body.Role == "" {
		writeError(w, http.StatusBadRequest, "user_id and role are required")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	if _, err := rc.GrantRole(r.Context(), &authv1.GrantRoleRequest{
		TenantId:   tenantID,
		UserId:     body.UserID,
		Role:       body.Role,
		ScopeType:  "repo",
		ScopeValue: org + "/" + repoName,
		GrantedBy:  callerID,
	}); err != nil {
		slog.Error("GrantRole (repo)", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to grant role")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeRepoMember removes a specific repo role assignment.
func (h *Handler) handleRevokeRepoMember(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")
	assignmentID := r.PathValue("assignmentID")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Admin or above may revoke repo-level roles.
	roles := h.getUserRoles(r)
	if !hasRole(roles, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	rc := h.rbacClient()
	if rc == nil {
		writeError(w, http.StatusInternalServerError, "rbac client unavailable")
		return
	}

	if _, err := rc.RevokeRole(r.Context(), &authv1.RevokeRoleRequest{
		TenantId:     tenantID,
		AssignmentId: assignmentID,
	}); err != nil {
		slog.Error("RevokeRole (repo)", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke role")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// memberSlice converts a slice of proto RoleAssignment messages to JSON-serialisable structs.
func memberSlice(proto []*authv1.RoleAssignment) []MemberResponse {
	if len(proto) == 0 {
		return []MemberResponse{}
	}
	out := make([]MemberResponse, len(proto))
	for i, m := range proto {
		out[i] = MemberResponse{
			ID:         m.GetId(),
			UserID:     m.GetUserId(),
			Role:       m.GetRole(),
			ScopeType:  m.GetScopeType(),
			ScopeValue: m.GetScopeValue(),
			GrantedBy:  m.GetGrantedBy(),
		}
	}
	return out
}
