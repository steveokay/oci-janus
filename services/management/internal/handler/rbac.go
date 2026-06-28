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
	"strings"

	"github.com/steveokay/oci-janus/libs/config/loader"
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

// hasScopedRole returns true when at least one assignment grants the user a
// role at or above `minimum` for the specified target scope (PENTEST-002).
//
// Containment rule: an org-scoped grant implicitly covers every repo within
// that org. So an admin of "myorg" can manage "myorg/myimage". A repo-scoped
// grant does NOT cover the parent org or sibling repos.
//
// Target scope:
//   - scopeType="org",  scopeValue="myorg"           — matches org-scoped grants on "myorg"
//   - scopeType="repo", scopeValue="myorg/myimage"   — matches that repo OR an org grant on "myorg"
func hasScopedRole(assignments []*authv1.RoleAssignment, scopeType, scopeValue, minimum string) bool {
	minIdx := roleIndex(minimum)
	if minIdx < 0 {
		return false
	}
	for _, a := range assignments {
		if roleIndex(a.GetRole()) < minIdx {
			continue
		}
		if a.GetScopeType() == scopeType && a.GetScopeValue() == scopeValue {
			return true
		}
		// Org grant covers all repos within that org.
		if scopeType == "repo" && a.GetScopeType() == "org" {
			if strings.HasPrefix(scopeValue, a.GetScopeValue()+"/") {
				return true
			}
		}
	}
	return false
}

// effectiveTenantAdmin returns true if the caller has admin authority over
// tenant-wide settings (webhooks, scan policies, audit export, etc.).
//
// Two paths qualify:
//   - Platform-admin marker (admin, org, "*") — the legacy convention;
//     Phase 5.1 replaces this with users.is_global_admin column. The legacy
//     marker check is retained here as a fallback during the migration window
//     in case any marker grants survived the backfill (e.g. in test envs).
//   - Tenant-scoped admin (admin, tenant, <tenant_id>) — introduced by
//     migration 20260625000001.
//
// Critically: does NOT return true for org-scoped admins of any org in the
// tenant. An org-A admin must NOT be able to configure tenant-wide settings
// that affect org-B (Review §A1, Top-5 #2 fix).
func effectiveTenantAdmin(assignments []*authv1.RoleAssignment, tenantID string) bool {
	// Platform-admin marker: (admin, org, "*") — legacy fallback. After
	// Phase 5.1 backfill this will match no rows; the field is kept so
	// partially-migrated environments don't lose admin access during the
	// upgrade window.
	if hasScopedRole(assignments, "org", "*", "admin") {
		return true
	}
	// Tenant-scoped admin — the new scope_type introduced in migration
	// 20260625000001_add_tenant_scope.sql.
	return hasScopedRole(assignments, "tenant", tenantID, "admin")
}

// effectiveGlobalAdmin returns true if the caller has platform-admin authority.
//
// Two paths qualify:
//   - users.is_global_admin = true (Phase 5.1 typed primitive). This is the
//     canonical gate going forward — it is set/cleared exclusively via
//     SetGlobalAdmin and cannot be minted by calling GrantRole with scope='*'.
//   - In DEPLOYMENT_MODE=single, ANY tenant-admin grant qualifies because the
//     deployment IS the platform — there is no meaningful distinction between
//     "this tenant's admin" and "the platform's admin" when there's only one
//     tenant.
//
// In multi mode the distinction is preserved: only the typed flag qualifies.
//
// Single source of truth for "is this caller allowed to touch platform-level
// surfaces" (scanner adapters, GC, deployment info, cross-tenant management).
// REDESIGN-001 Phase 5.1.
func (h *Handler) effectiveGlobalAdmin(r *http.Request) bool {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	// Fetch the full permissions response — it includes is_global_admin (Phase 5.1)
	// alongside role_assignments (PENTEST-002). A nil/error response fails closed.
	resp, err := h.auth.GetUserPermissions(r.Context(), &authv1.GetUserPermissionsRequest{
		UserId:   userID,
		TenantId: tenantID,
	})
	if err != nil {
		slog.Warn("effectiveGlobalAdmin: GetUserPermissions failed", "err", err)
		return false
	}

	// Typed column path (Phase 5.1) — the preferred check.
	if resp.GetIsGlobalAdmin() {
		return true
	}

	// Single-mode shortcut: any tenant-admin qualifies as platform-admin when
	// the whole deployment is a single tenant.
	if h.deploymentMode == loader.DeploymentModeSingle {
		return hasScopedRole(resp.GetRoleAssignments(), "tenant", tenantID, "admin")
	}

	return false
}

// getUserAssignments fetches the caller's full role-assignment list for the
// current tenant. Returns nil on error so the fail-closed behaviour in
// hasScopedRole (returns false on empty list) holds.
func (h *Handler) getUserAssignments(r *http.Request) []*authv1.RoleAssignment {
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
	return resp.GetRoleAssignments()
}

// (PENTEST-024 cleanup) — getUserRoles and the flat-role hasRole helper used
// to live here. They were removed once the last unsafe caller in
// handleSetTenantQuota was migrated to hasScopedRole with the platform-admin
// marker scope. Re-introducing a flat-role check would re-open the PENTEST-002
// privilege-escalation class.

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
//
// REM-018 enrichment: the FE used to render raw UUIDs in the members table
// and the granted-by column. The auth service is the system of record for
// the username/display_name join, so it surfaces both fields inline and the
// BFF passes them through verbatim. The FE UserCell primitive prefers
// DisplayName over Username, falling back to a shortened UUID only when
// both are empty (which shouldn't happen for valid users).
//
// GrantedBy* fields are empty strings when the assignment was created by
// the system (granted_by is the zero UUID). The FE renders a "system"
// placeholder in that case rather than poking at the UUID.
type MemberResponse struct {
	ID                   string `json:"id"`
	UserID               string `json:"user_id"`
	Username             string `json:"username"`
	DisplayName          string `json:"display_name"`
	Role                 string `json:"role"`
	ScopeType            string `json:"scope_type"`
	ScopeValue           string `json:"scope_value"`
	GrantedBy            string `json:"granted_by"`
	GrantedByUsername    string `json:"granted_by_username"`
	GrantedByDisplayName string `json:"granted_by_display_name"`
}

// handleListOrgMembers returns all role assignments for the given org scope.
// PENTEST-006: requires at least reader on the org so non-members cannot
// enumerate the membership list.
func (h *Handler) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	if !hasScopedRole(h.getUserAssignments(r), "org", org, "reader") {
		// 404 (not 403) so non-members cannot confirm the org exists.
		writeError(w, http.StatusNotFound, "org not found")
		return
	}

	resp, err := h.auth.ListMembers(r.Context(), &authv1.ListMembersRequest{
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

	// PENTEST-002: only admin/owner OF THIS ORG (not anywhere in the tenant) may grant.
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
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

	if _, err := h.auth.GrantRole(r.Context(), &authv1.GrantRoleRequest{
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

	// PENTEST-002: only admin/owner OF THIS ORG may revoke.
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// PENTEST-011: pass the expected scope so the auth service refuses to
	// delete an assignment whose scope mismatches this URL — defends against
	// "admin of org-A passes a different org's assignment ID".
	if _, err := h.auth.RevokeRole(r.Context(), &authv1.RevokeRoleRequest{
		TenantId:           tenantID,
		AssignmentId:       assignmentID,
		ExpectedScopeType:  "org",
		ExpectedScopeValue: org,
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

	// PENTEST-006: require at least reader on this repo (or its parent org).
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	resp, err := h.auth.ListMembers(r.Context(), &authv1.ListMembersRequest{
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

	// PENTEST-002: admin on this repo OR admin on the parent org may grant.
	scopeValue := org + "/" + repoName
	if !hasScopedRole(h.getUserAssignments(r), "repo", scopeValue, "admin") {
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

	if _, err := h.auth.GrantRole(r.Context(), &authv1.GrantRoleRequest{
		TenantId:   tenantID,
		UserId:     body.UserID,
		Role:       body.Role,
		ScopeType:  "repo",
		ScopeValue: scopeValue,
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

	// PENTEST-002: admin on this repo OR admin on the parent org may revoke.
	scopeValue := org + "/" + repoName
	if !hasScopedRole(h.getUserAssignments(r), "repo", scopeValue, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// PENTEST-011: scope-verification fields force the auth service to refuse
	// an assignment whose scope mismatches this URL.
	if _, err := h.auth.RevokeRole(r.Context(), &authv1.RevokeRoleRequest{
		TenantId:           tenantID,
		AssignmentId:       assignmentID,
		ExpectedScopeType:  "repo",
		ExpectedScopeValue: scopeValue,
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
			ID:                   m.GetId(),
			UserID:               m.GetUserId(),
			Username:             m.GetUsername(),
			DisplayName:          m.GetDisplayName(),
			Role:                 m.GetRole(),
			ScopeType:            m.GetScopeType(),
			ScopeValue:           m.GetScopeValue(),
			GrantedBy:            m.GetGrantedBy(),
			GrantedByUsername:    m.GetGrantedByUsername(),
			GrantedByDisplayName: m.GetGrantedByDisplayName(),
		}
	}
	return out
}
