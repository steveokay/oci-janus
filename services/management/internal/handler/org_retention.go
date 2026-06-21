// Package handler — FE-API-039 per-org default retention policy routes.
//
// Three routes mounted under /api/v1/orgs/{org}/policies/retention:
//
//	GET    — any user with org `reader` or above. NotFound (404 with code
//	         "no-org-default") when no default exists; the UI uses that to
//	         render the empty state.
//	PUT    — org admin or owner only. Writer is NOT enough because retention
//	         is a destructive primitive — the same gate as the per-repo PUT.
//	DELETE — org admin or owner only. 204 on success, 404 when no default.
//
// updated_by is taken from the JWT (not the body), preview_until is owned by
// the metadata layer and is not settable from the wire — same rules as the
// per-repo CRUD (see repo_retention.go).
//
// The JSON shape reuses RetentionPolicyResponse with the `org_id` field set
// and `repo_id` empty, so dashboard code can render both per-repo and
// per-org defaults through the same component.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// RegisterOrgRetention mounts the FE-API-039 org-default retention routes.
// Called from Handler.Register alongside RegisterRepoRetention so the route
// table stays grouped by ticket.
func (h *Handler) RegisterOrgRetention(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/orgs/{org}/policies/retention",
		authMW(http.HandlerFunc(h.handleGetOrgRetention)))
	mux.Handle("PUT /api/v1/orgs/{org}/policies/retention",
		authMW(http.HandlerFunc(h.handlePutOrgRetention)))
	mux.Handle("DELETE /api/v1/orgs/{org}/policies/retention",
		authMW(http.HandlerFunc(h.handleDeleteOrgRetention)))
}

// resolveOrgID maps an org name (from the URL) to its UUID. Returns the
// boolean false when the caller has already written a 404 to w — handlers
// short-circuit on false. We do NOT bubble a typed error out because every
// caller wants the same "translate to 404" behaviour.
//
// Auth note: we deliberately defer this lookup until AFTER the role gate so a
// non-member can't probe org existence via timing differences (the role gate
// returns 404 for the missing-role case too, masking existence either way).
func (h *Handler) resolveOrgID(w http.ResponseWriter, r *http.Request, tenantID, orgName string) (string, bool) {
	resp, err := h.meta.LookupOrgIDByName(r.Context(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: tenantID,
		Name:     orgName,
	})
	if err != nil {
		if grpcCodeOf(err) == codes.NotFound {
			writeError(w, http.StatusNotFound, "org not found")
			return "", false
		}
		slog.Error("LookupOrgIDByName", "err", err, "org", orgName)
		writeError(w, http.StatusInternalServerError, "failed to resolve org")
		return "", false
	}
	return resp.GetOrgId(), true
}

// handleGetOrgRetention returns the org's default retention policy. NotFound
// surfaces with a typed body so the UI can distinguish "no default set" from
// other 404 paths.
func (h *Handler) handleGetOrgRetention(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	// Reader on the org is sufficient to view the default. The role gate
	// runs BEFORE the org_id lookup so a non-member can't enumerate org
	// existence via timing (404 either way for them).
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "reader") {
		// 404 (not 403) so non-members cannot confirm the org exists.
		writeError(w, http.StatusNotFound, "org not found")
		return
	}

	orgID, ok := h.resolveOrgID(w, r, tenantID, org)
	if !ok {
		return
	}

	policy, err := h.meta.GetOrgRetentionPolicy(r.Context(), &metadatav1.GetOrgRetentionPolicyRequest{
		TenantId: tenantID,
		OrgId:    orgID,
	})
	if err != nil {
		if grpcCodeOf(err) == codes.NotFound {
			// Typed 404 so the UI can render the empty state cleanly. We use
			// "no-org-default" (not "no-policy") so a dashboard widget can
			// disambiguate per-repo vs. per-org absence states.
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "no-org-default",
				"message": "no default retention policy on this org",
			})
			return
		}
		slog.Error("GetOrgRetentionPolicy", "err", err, "org_id", orgID)
		writeError(w, http.StatusInternalServerError, "failed to fetch org default retention policy")
		return
	}
	// inherited_from="org" so dashboard code can render the source label
	// consistently whether the policy was fetched via the per-repo GET
	// (inheritance) or the per-org GET (direct).
	writeJSON(w, http.StatusOK, retentionPolicyToResponseWith(policy, "org"))
}

// handlePutOrgRetention writes or replaces the org default. JWT user_id is
// forwarded as updated_by — clients cannot override it.
func (h *Handler) handlePutOrgRetention(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	// Retention is destructive — eventually evicts manifests across every
	// repo that inherits this default. Writer isn't enough; require org
	// admin (which owner satisfies via the role hierarchy).
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	orgID, ok := h.resolveOrgID(w, r, tenantID, org)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body updateRetentionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Map the wire shape to proto. Authoritative validation lives in the
	// metadata gRPC handler so the two PUT routes share rules.
	protoRules := make([]*metadatav1.RetentionRule, 0, len(body.Rules))
	for _, rule := range body.Rules {
		protoRules = append(protoRules, &metadatav1.RetentionRule{
			Kind:  rule.Kind,
			Value: rule.Value,
		})
	}
	patterns := body.ProtectedTagPatterns
	if patterns == nil {
		patterns = []string{}
	}

	policy, err := h.meta.UpsertOrgRetentionPolicy(r.Context(), &metadatav1.UpsertOrgRetentionPolicyRequest{
		TenantId:             tenantID,
		OrgId:                orgID,
		Enabled:              body.Enabled,
		Rules:                protoRules,
		ProtectedTagPatterns: patterns,
		UpdatedBy:            userID,
	})
	if err != nil {
		switch grpcCodeOf(err) {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, "invalid retention policy")
		case codes.NotFound:
			// Org deleted between resolveOrgID and the upsert (race).
			writeError(w, http.StatusNotFound, "org not found")
		default:
			slog.Error("UpsertOrgRetentionPolicy", "err", err, "org_id", orgID)
			writeError(w, http.StatusInternalServerError, "failed to update org default retention policy")
		}
		return
	}
	writeJSON(w, http.StatusOK, retentionPolicyToResponseWith(policy, "org"))
}

// handleDeleteOrgRetention removes the default. 204 on success; 404 when no
// default exists. Repos that previously inherited fall back to "no policy"
// (the per-repo GET returns the existing 404 "no-policy" body).
func (h *Handler) handleDeleteOrgRetention(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	orgID, ok := h.resolveOrgID(w, r, tenantID, org)
	if !ok {
		return
	}

	if _, err := h.meta.DeleteOrgRetentionPolicy(r.Context(), &metadatav1.DeleteOrgRetentionPolicyRequest{
		TenantId: tenantID,
		OrgId:    orgID,
	}); err != nil {
		if grpcCodeOf(err) == codes.NotFound {
			writeError(w, http.StatusNotFound, "no default retention policy on this org")
			return
		}
		slog.Error("DeleteOrgRetentionPolicy", "err", err, "org_id", orgID)
		writeError(w, http.StatusInternalServerError, "failed to delete org default retention policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
