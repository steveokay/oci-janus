// Package handler — /api/v1/me/abilities endpoint.
//
// handleAbilities returns the caller's role assignments + is_global_admin
// flag so the FE can use the same containment rule the BFF enforces in
// hasScopedRole. Closes Review §C2 + §D3 — eliminates FE/BE RBAC drift
// since the FE was previously inferring role from a flat claims.roles list
// that lost scope information.
//
// REDESIGN-001 Phase 4.4. Authenticated; the caller may only see their own
// abilities (no impersonation).
package handler

import (
	"log/slog"
	"net/http"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// abilitiesResponse is the JSON shape for GET /api/v1/me/abilities.
//
// The FE useAbility() hook mirrors the containment rule applied here
// (REDESIGN-001 Phase 4.4) — is_global_admin bypasses all scope checks;
// role_assignments carry the scoped grants the BFF evaluates via hasScopedRole.
type abilitiesResponse struct {
	// IsGlobalAdmin reflects the users.is_global_admin typed column (Phase 5.1).
	// True grants all abilities regardless of role_assignments.
	IsGlobalAdmin bool `json:"is_global_admin"`
	// RoleAssignments is the ordered list of scoped grants for the caller in
	// their current tenant. The FE applies the same containment rule as
	// hasScopedRole to evaluate "does the caller hold ≥role on this scope?".
	RoleAssignments []abilityAssignment `json:"role_assignments"`
}

// abilityAssignment is one scoped role grant for the caller.
type abilityAssignment struct {
	// Role is one of "reader" | "writer" | "admin" | "owner".
	Role string `json:"role"`
	// ScopeType is one of "org" | "repo" | "tenant".
	ScopeType string `json:"scope_type"`
	// ScopeValue is the concrete scope identifier:
	//   org  → "myorg"
	//   repo → "myorg/myimage"
	//   tenant → "<tenant_uuid>"
	ScopeValue string `json:"scope_value"`
}

// handleAbilities returns the caller's role assignments + is_global_admin flag.
//
// Uses the same GetUserPermissions RPC that hasScopedRole callers use internally
// so the FE receives exactly the data the BFF already gates on — no parallel
// permission system, no drift.
//
// Authenticated (authMW required on the route). No impersonation: the caller
// always sees their own abilities, keyed off the context user_id + tenant_id
// injected by RequireAuth.
func (h *Handler) handleAbilities(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.auth.GetUserPermissions(r.Context(), &authv1.GetUserPermissionsRequest{
		UserId:   userID,
		TenantId: tenantID,
	})
	if err != nil {
		slog.WarnContext(r.Context(), "handleAbilities: GetUserPermissions failed", "err", err)
		writeError(w, http.StatusInternalServerError, "permissions lookup failed")
		return
	}

	body := abilitiesResponse{
		IsGlobalAdmin:   resp.GetIsGlobalAdmin(),
		RoleAssignments: convertAbilityAssignments(resp.GetRoleAssignments()),
	}
	writeJSON(w, http.StatusOK, body)
}

// convertAbilityAssignments translates proto RoleAssignment slices to the
// JSON-serialisable abilityAssignment shape. Returns an empty (non-nil) slice
// when the input is empty so the JSON output is always [] not null.
func convertAbilityAssignments(in []*authv1.RoleAssignment) []abilityAssignment {
	out := make([]abilityAssignment, 0, len(in))
	for _, a := range in {
		out = append(out, abilityAssignment{
			Role:       a.GetRole(),
			ScopeType:  a.GetScopeType(),
			ScopeValue: a.GetScopeValue(),
		})
	}
	return out
}
