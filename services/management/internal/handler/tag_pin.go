// Package handler — tag_pin.go
//
// Futures.md Tier 1 #2 — Tag immutability + image promotion workflow.
//
// Two routes that wrap the metadata UpdateTagImmutable RPC behind a
// repo admin/owner gate. The actual immutability enforcement lives in
// services/core (PutManifest preflight); these routes only flip the
// per-tag `immutable` flag.
//
//   POST   /api/v1/repositories/{org}/{repo}/tags/{tag}/pin   — set true
//   DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}/pin   — set false
//
// Permissions mirror the retention + scan policy editors: repo admin
// or above. A writer can push to existing mutable tags but can't
// unilaterally pin them down or remove a pin — that's an audit-
// significant change, gated to people who own the repo.
package handler

import (
	"log/slog"
	"net/http"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// handlePinTag sets `tags.immutable = true` so subsequent pushes that
// would move this tag are rejected with MANIFEST_INVALID.
func (h *Handler) handlePinTag(w http.ResponseWriter, r *http.Request) {
	h.setTagImmutable(w, r, true)
}

// handleUnpinTag sets `tags.immutable = false`. The parent repository's
// `immutable_tags` flag (if set) still applies after the pin is removed;
// the pin operation only clears the per-tag layer.
func (h *Handler) handleUnpinTag(w http.ResponseWriter, r *http.Request) {
	h.setTagImmutable(w, r, false)
}

// setTagImmutable is the shared body of both routes. Extracted so the
// POST/DELETE difference is one boolean and the rest of the validation +
// permission gate + RPC fan-out lives in one place.
func (h *Handler) setTagImmutable(w http.ResponseWriter, r *http.Request, immutable bool) {
	tenantID := middleware.TenantIDFromContext(r.Context())
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

	// Repo admin / owner gate — same posture as retention + scan policy
	// editors. Pinning gates pushes; the caller needs the security-
	// relevant role to flip it.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	tag, err := h.meta.UpdateTagImmutable(r.Context(), &metadatav1.UpdateTagImmutableRequest{
		TenantId:  tenantID,
		RepoId:    repo.GetRepoId(),
		Name:      tagName,
		Immutable: immutable,
	})
	if err != nil {
		slog.Error("UpdateTagImmutable", "err", err, "repo", org+"/"+repoName, "tag", tagName, "immutable", immutable)
		writeError(w, http.StatusInternalServerError, "failed to update tag pin")
		return
	}

	// Surface the updated tag verbatim so the FE doesn't need a follow-up
	// GET to repaint the row — same pattern as the quarantine lift route.
	out := TagResponse{
		Name:           tag.GetName(),
		ManifestDigest: tag.GetManifestDigest(),
		SizeBytes:      tag.GetSizeBytes(),
		UpdatedAt:      tag.GetUpdatedAt().AsTime(),
		CreatedAt:      tag.GetCreatedAt().AsTime(),
		Quarantined:    tag.GetQuarantined(),
		ArtifactType:   tag.GetArtifactType(),
		Immutable:      tag.GetImmutable(),
	}
	if ts := tag.GetRetentionPendingDeleteAt(); ts != nil {
		t := ts.AsTime()
		out.RetentionPendingDeleteAt = &t
	}
	writeJSON(w, http.StatusOK, out)
}
