// Package handler — platform-admin "claim a new org" endpoint.
//
// Bootstrap problem this solves: org creation is a side effect of repo
// creation, and repo creation requires hasScopedRole("org", body.Org, "admin").
// The platform-admin marker grant `(admin, org, "*")` is a literal scope, not
// a wildcard (PENTEST-024 — deliberate, preserves the per-org isolation
// property for repo CRUD). So platform admins cannot bootstrap a new org from
// the FE without first running `INSERT INTO role_assignments...` via SQL.
//
// This route closes the gap: a platform admin POSTs to
//
//	POST /api/v1/admin/orgs/{org}/claim
//
// and the BFF grants them admin on the specified org. From there they can use
// the existing /api/v1/repositories flow. The org row in metadata is created
// lazily as a side effect of the first CreateRepository call (the metadata
// service's GetOrCreateOrganization is invoked when CreateRepository sees a
// name in "org/repo" form with no org_id supplied) — no separate ensure-org
// gRPC is required.
//
// Idempotent: re-claiming the same org by the same caller is a no-op, courtesy
// of role_assignments' UNIQUE (user_id, role_id, scope_type, scope_value) +
// ON CONFLICT DO NOTHING inside services/auth. Returns 201 in both fresh and
// re-claim cases so the FE doesn't need to special-case.
package handler

import (
	"log/slog"
	"net/http"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// adminClaimOrgResponse is the JSON body returned on a successful claim.
type adminClaimOrgResponse struct {
	Org         string `json:"org"`
	GrantedRole string `json:"granted_role"`
}

// handleAdminClaimOrg grants the calling platform admin the `admin` role on
// the org named in the URL. The org name is validated against the standard
// validateOrgName regex — the literal "*" platform-admin marker cannot leak
// in here because `*` is not in the [a-z0-9-] allowlist.
//
// Gating: caller must be an effective global admin. Phase 5.1 (PR #134)
// replaced the legacy (admin, org, "*") role marker with the typed
// users.is_global_admin column; the equivalent check now lives in
// h.effectiveGlobalAdmin (rbac.go). The per-org strict-match used by
// handleCreateRepository (PENTEST-002 + PENTEST-024) is unchanged.
func (h *Handler) handleAdminClaimOrg(w http.ResponseWriter, r *http.Request) {
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return
	}

	org := r.PathValue("org")
	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())

	if _, err := h.auth.GrantRole(r.Context(), &authv1.GrantRoleRequest{
		TenantId:   tenantID,
		UserId:     callerID,
		Role:       "admin",
		ScopeType:  "org",
		ScopeValue: org,
		GrantedBy:  callerID,
	}); err != nil {
		slog.Error("admin: GrantRole on org claim", "err", err, "org", org, "user_id", callerID)
		writeError(w, http.StatusInternalServerError, "failed to claim org")
		return
	}

	writeJSON(w, http.StatusCreated, adminClaimOrgResponse{
		Org:         org,
		GrantedRole: "admin",
	})
}
