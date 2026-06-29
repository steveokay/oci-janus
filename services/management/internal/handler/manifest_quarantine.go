// Package handler — FE-API-050 manifest quarantine surface.
//
// One route today, scoped per-tag:
//
//	POST /api/v1/repositories/{org}/{repo}/tags/{tag}/quarantine/lift
//
// The scanner sets quarantine automatically based on the effective
// scan policy (services/scanner/internal/worker/worker.go runJob calls
// metadata.UpdateManifestQuarantine on policy violation). This route
// lets a repo admin / owner dismiss a quarantine after operator review
// — typical workflow is "vulnerability flagged, operator reviews the
// scan results, decides it's acceptable risk + lifts the gate". The
// lift writes through to metadata + busts the GetManifest cache so
// subsequent pulls succeed.
//
// We deliberately do NOT expose a "set quarantine" route from the BFF.
// Manual operator quarantines are a future feature; for now the only
// way to flip the flag is via the scanner (automatic policy
// enforcement) or via the lift route (operator dismissal). The
// asymmetric API surface is intentional — making manual quarantine
// trivial would invite the "denial of service via UI button" pattern
// that PENTEST-007 family flagged for the delete-repo flow.
package handler

import (
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// RegisterManifestQuarantine mounts the FE-API-050 lift route.
func (h *Handler) RegisterManifestQuarantine(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/tags/{tag}/quarantine/lift",
		authMW(http.HandlerFunc(h.handleLiftQuarantine)))
}

// handleLiftQuarantine clears the quarantine on the manifest pointed at
// by the tag. Repo admin / owner only — writer is not enough, because
// lifting bypasses the operator-configured security gate.
//
// Resolves tag → digest via the existing GetTag flow so the FE doesn't
// have to pass the digest separately; the operator clicks a button on
// the tag detail page, the BFF figures out the rest.
func (h *Handler) handleLiftQuarantine(w http.ResponseWriter, r *http.Request) {
	if h.meta == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	org, repoName, tagName := r.PathValue("org"), r.PathValue("repo"), r.PathValue("tag")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if err := validateTagName(tagName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag name")
		return
	}
	// Admin gate — lifting bypasses the security policy gate, so we
	// hold this to the same role as PUT scan-policy.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Resolve tag → digest. Reuses the metadata GetTag path so the
	// resolution stays in one place; a missing tag surfaces as 404.
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		Name:     tagName,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "tag not found")
			return
		}
		slog.Error("GetTag for lift quarantine", "err", err, "tag", tagName)
		writeError(w, http.StatusInternalServerError, "failed to resolve tag")
		return
	}

	// Clear the quarantine on the parent manifest. quarantined=false
	// makes the metadata handler ignore reason — we pass an empty
	// string for clarity. quarantined_by carries the operator's
	// user_id so the audit trail records who lifted.
	m, err := h.meta.UpdateManifestQuarantine(r.Context(), &metadatav1.UpdateManifestQuarantineRequest{
		TenantId:       tenantID,
		RepoId:         repo.GetRepoId(),
		ManifestDigest: tag.GetManifestDigest(),
		Quarantined:    false,
		Reason:         "",
		QuarantinedBy:  userID,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "manifest not found")
			return
		}
		slog.Error("UpdateManifestQuarantine lift", "err", err,
			"repo", org+"/"+repoName, "tag", tagName, "digest", tag.GetManifestDigest())
		writeError(w, http.StatusInternalServerError, "failed to lift quarantine")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"manifest_digest": m.GetDigest(),
		"quarantined":     m.GetQuarantined(),
	})
}
