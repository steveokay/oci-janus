// Package handler — FE-API-049 org-default + per-repo scan policy
// routes.
//
// Six new routes layered on the existing FE-API-018 per-tenant pair:
//
//	GET    /api/v1/orgs/{org}/policies/scan                 — org reader+
//	PUT    /api/v1/orgs/{org}/policies/scan                 — org admin/owner
//	DELETE /api/v1/orgs/{org}/policies/scan                 — org admin/owner
//	GET    /api/v1/repositories/{org}/{repo}/policies/scan  — repo reader+
//	PUT    /api/v1/repositories/{org}/{repo}/policies/scan  — repo admin/owner
//	DELETE /api/v1/repositories/{org}/{repo}/policies/scan  — repo admin/owner
//
// Writer is intentionally NOT enough on the writes — scan policy gates
// push admission via block_on_severity, and a writer who can push
// images should not be able to change the gate they're being pushed
// against. Same posture as the FE-API-037/039 retention CRUD.
//
// Validation reuses the FE-API-018 allowlists (allowedBlockSeverities,
// allowedScannerPlugins, reCVEID) so all three scopes accept the same
// shapes — no chance of a value passing at one tier and failing at
// another.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// upsertScopedScanPolicyBody is the PUT body for org and repo scopes.
// Same fields as updateScanPolicyBody plus Enabled — scoped policies
// can be flipped off without losing config (mirrors FE-API-039).
type upsertScopedScanPolicyBody struct {
	AutoScanOnPush    bool     `json:"auto_scan_on_push"`
	BlockOnSeverity   string   `json:"block_on_severity"`
	ExemptCVEs        []string `json:"exempt_cves"`
	ScannerPlugin     string   `json:"scanner_plugin"`
	ScannerVersionPin string   `json:"scanner_version_pin"`
	Enabled           bool     `json:"enabled"`
}

// RegisterOrgScanPolicy mounts the FE-API-049 org-default routes. Wired
// from Handler.Register alongside RegisterSecurityPolicies so route
// table additions stay grouped by feature.
func (h *Handler) RegisterOrgScanPolicy(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/orgs/{org}/policies/scan",
		authMW(http.HandlerFunc(h.handleGetOrgScanPolicy)))
	mux.Handle("PUT /api/v1/orgs/{org}/policies/scan",
		authMW(http.HandlerFunc(h.handlePutOrgScanPolicy)))
	mux.Handle("DELETE /api/v1/orgs/{org}/policies/scan",
		authMW(http.HandlerFunc(h.handleDeleteOrgScanPolicy)))
}

// RegisterRepoScanPolicy mounts the per-repo override routes.
func (h *Handler) RegisterRepoScanPolicy(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/policies/scan",
		authMW(http.HandlerFunc(h.handleGetRepoScanPolicy)))
	mux.Handle("PUT /api/v1/repositories/{org}/{repo}/policies/scan",
		authMW(http.HandlerFunc(h.handlePutRepoScanPolicy)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/policies/scan",
		authMW(http.HandlerFunc(h.handleDeleteRepoScanPolicy)))
}

// ─── Org-default scope ───────────────────────────────────────────────

// handleGetOrgScanPolicy returns the org default. 404 with the typed
// "no-policy" body when none exists so the dashboard can render an
// explicit empty state. Reader+ on the org is enough — the response is
// non-secret config.
func (h *Handler) handleGetOrgScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")
	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	// Reader on the org is sufficient for the read path. 404 (not 403)
	// when missing so non-members can't probe org existence via timing.
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "reader") {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	orgID, ok := h.resolveOrgID(w, r, tenantID, org)
	if !ok {
		return
	}

	p, err := h.scanner.GetOrgScanPolicy(r.Context(), &scannerv1.GetOrgScanPolicyRequest{
		TenantId: tenantID,
		OrgId:    orgID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.NotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "no-policy",
				"message": "no scan policy on this org",
			})
			return
		}
		slog.Error("GetOrgScanPolicy", "err", err, "org_id", orgID)
		writeError(w, http.StatusInternalServerError, "failed to fetch org scan policy")
		return
	}
	writeJSON(w, http.StatusOK, scanPolicyToResponse(p))
}

// handlePutOrgScanPolicy upserts the org default. Org admin/owner only.
func (h *Handler) handlePutOrgScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	org := r.PathValue("org")
	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	// Writer is not enough — scan policy gates push admission, so a
	// writer who can push images shouldn't be able to change the gate.
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	orgID, ok := h.resolveOrgID(w, r, tenantID, org)
	if !ok {
		return
	}

	body, ok := decodeScopedScanPolicyBody(w, r)
	if !ok {
		return
	}

	p, err := h.scanner.UpsertOrgScanPolicy(r.Context(), &scannerv1.UpsertOrgScanPolicyRequest{
		TenantId:          tenantID,
		OrgId:             orgID,
		AutoScanOnPush:    body.AutoScanOnPush,
		BlockOnSeverity:   body.BlockOnSeverity,
		ExemptCves:        body.ExemptCVEs,
		ScannerPlugin:     body.ScannerPlugin,
		ScannerVersionPin: body.ScannerVersionPin,
		Enabled:           body.Enabled,
		UpdatedBy:         userID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		switch st.Code() {
		case codes.InvalidArgument:
			slog.Warn("UpsertOrgScanPolicy invalid argument", "detail", st.Message())
			writeError(w, http.StatusBadRequest, "invalid request")
		default:
			slog.Error("UpsertOrgScanPolicy", "err", err, "org_id", orgID)
			writeError(w, http.StatusInternalServerError, "failed to update org scan policy")
		}
		return
	}
	writeJSON(w, http.StatusOK, scanPolicyToResponse(p))
}

// handleDeleteOrgScanPolicy removes the org default. 204 on success;
// 404 when no row existed so the caller knows whether they actually
// cleared something.
func (h *Handler) handleDeleteOrgScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
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

	if _, err := h.scanner.DeleteOrgScanPolicy(r.Context(), &scannerv1.DeleteOrgScanPolicyRequest{
		TenantId: tenantID,
		OrgId:    orgID,
	}); err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "no scan policy on this org")
			return
		}
		slog.Error("DeleteOrgScanPolicy", "err", err, "org_id", orgID)
		writeError(w, http.StatusInternalServerError, "failed to delete org scan policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Per-repo override scope ─────────────────────────────────────────

// handleGetRepoScanPolicy returns the per-repo override OR — when no
// override exists — the inherited org/tenant/default policy via
// GetEffectiveScanPolicy. The dashboard renders the response as either
// "this repo has its own policy" or "this repo inherits from <source>"
// based on the inherited_from label.
func (h *Handler) handleGetRepoScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
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
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Resolve via GetEffectiveScanPolicy so the response always carries
	// a policy + an inherited_from label. The scanner walks the chain
	// itself; the BFF doesn't need a separate "per-repo first" call.
	eff, err := h.scanner.GetEffectiveScanPolicy(r.Context(), &scannerv1.GetEffectiveScanPolicyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		slog.Error("GetEffectiveScanPolicy", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to fetch effective scan policy")
		return
	}
	out := scanPolicyToResponse(eff.GetPolicy())
	out.InheritedFrom = eff.GetInheritedFrom()
	writeJSON(w, http.StatusOK, out)
}

// handlePutRepoScanPolicy upserts a per-repo override. Repo admin/owner.
// We resolve repo→org via the metadata service first so the per-repo
// row carries an accurate org_id for the inheritance helper.
func (h *Handler) handlePutRepoScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")
	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	body, ok := decodeScopedScanPolicyBody(w, r)
	if !ok {
		return
	}

	// Resolve repo→org once so the per-repo row has the right org_id
	// for the inheritance helper (avoids a metadata round-trip on every
	// push.completed event).
	orgLookup, err := h.meta.LookupOrgIDByName(r.Context(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: tenantID,
		Name:     org,
	})
	if err != nil {
		slog.Error("LookupOrgIDByName", "err", err, "org", org)
		writeError(w, http.StatusInternalServerError, "failed to resolve org id")
		return
	}

	p, err := h.scanner.UpsertRepoScanPolicy(r.Context(), &scannerv1.UpsertRepoScanPolicyRequest{
		TenantId:          tenantID,
		RepoId:            repo.GetRepoId(),
		AutoScanOnPush:    body.AutoScanOnPush,
		BlockOnSeverity:   body.BlockOnSeverity,
		ExemptCves:        body.ExemptCVEs,
		ScannerPlugin:     body.ScannerPlugin,
		ScannerVersionPin: body.ScannerVersionPin,
		Enabled:           body.Enabled,
		UpdatedBy:         userID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		switch st.Code() {
		case codes.InvalidArgument:
			slog.Warn("UpsertRepoScanPolicy invalid argument", "detail", st.Message())
			writeError(w, http.StatusBadRequest, "invalid request")
		default:
			slog.Error("UpsertRepoScanPolicy", "err", err, "repo_id", repo.GetRepoId())
			writeError(w, http.StatusInternalServerError, "failed to update repo scan policy")
		}
		return
	}
	// Echo OrgID into the response so the FE doesn't need a second
	// round-trip to know which org this repo's override is anchored
	// under. (The scanner row stores it; we just don't surface it
	// directly through the proto since the per-repo struct doesn't
	// have a Scan helper that returns OrgID. The BFF supplies it from
	// the lookup we just made.)
	out := scanPolicyToResponse(p)
	if out.OrgID == "" {
		out.OrgID = orgLookup.GetOrgId()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteRepoScanPolicy removes a per-repo override. Repo
// admin/owner. 204 on success, 404 when no override existed.
func (h *Handler) handleDeleteRepoScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
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
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	if _, err := h.scanner.DeleteRepoScanPolicy(r.Context(), &scannerv1.DeleteRepoScanPolicyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	}); err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "no scan policy override on this repository")
			return
		}
		slog.Error("DeleteRepoScanPolicy", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to delete repo scan policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Shared body decoding + validation ──────────────────────────────

// decodeScopedScanPolicyBody parses + validates the PUT body for both
// scopes. Returns false (and writes an error response) on any failure
// so callers stay simple. Allowlists are the same ones the per-tenant
// PUT uses — no chance of drift.
func decodeScopedScanPolicyBody(w http.ResponseWriter, r *http.Request) (upsertScopedScanPolicyBody, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body upsertScopedScanPolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return body, false
	}
	if _, ok := allowedBlockSeverities[body.BlockOnSeverity]; !ok {
		writeError(w, http.StatusBadRequest, "block_on_severity must be empty or one of CRITICAL,HIGH,MEDIUM,LOW")
		return body, false
	}
	if _, ok := allowedScannerPlugins[body.ScannerPlugin]; !ok {
		writeError(w, http.StatusBadRequest, "scanner_plugin must be one of trivy,grype")
		return body, false
	}
	for _, cve := range body.ExemptCVEs {
		if !reCVEID.MatchString(cve) {
			writeError(w, http.StatusBadRequest, "exempt_cves entries must match ^CVE-\\d{4}-\\d{4,7}$")
			return body, false
		}
	}
	if body.ExemptCVEs == nil {
		body.ExemptCVEs = []string{}
	}
	return body, true
}
