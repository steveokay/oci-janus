// Package handler — referrers.go
//
// Referrers tab — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/referrers
//
// Lists the OCI referrers of a tag: artifacts (SBOMs, signatures, scan
// results, attestations) whose `subject` points at the tag's current
// manifest digest. registry-core owns the OCI Referrers API
// (`GET /v2/<name>/referrers/<digest>`); this BFF route resolves the tag to
// its manifest digest via registry-metadata, then fans the lookup out to
// registry-core over gRPC so the dashboard never has to speak the OCI wire
// protocol directly.
//
// Authorization mirrors the sibling tag-detail routes (handleGetManifest /
// handleGetSignature): pull access on the repo, enforced by RequireAuth +
// the shared findRepo lookup. No extra role gate — the referrers are no more
// sensitive than the manifest a reader could already pull.
//
// The route is only useful when registry-core is reachable over gRPC. Like
// the other optional-service surfaces (signature, gc, proxy-cache), the
// handler returns 404 "route disabled" when the core client is nil so the
// frontend can hide the Referrers tab instead of rendering an error.
//
// Lives in its own file so concurrent edits to handler.go don't conflict
// with the referrers feature surface.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// listReferrersTimeout bounds the outgoing registry-core gRPC call so a slow
// or wedged core service can't hold the HTTP request open past the dashboard's
// patience. Matches the 5s deadline convention from CLAUDE.md §6 used across
// the other BFF → backend calls.
const listReferrersTimeout = 5 * time.Second

// ReferrersResponse is the JSON body for GET …/tags/{tag}/referrers.
//
// `Referrers` is always a non-nil slice so it serialises as `[]` (never
// `null`) when a tag has no referrers — the frontend binds the array
// directly without a null guard. `Filtered` echoes the core service's flag:
// true when the upstream applied an artifactType filter to the result set
// (reserved for a future ?artifact_type= query param; today the BFF always
// requests the unfiltered set).
type ReferrersResponse struct {
	Referrers []referrerRecord `json:"referrers"`
	Filtered  bool             `json:"filtered"`
}

// referrerRecord is one entry in ReferrersResponse.Referrers. Field names use
// snake_case to match the rest of the BFF wire contract so the frontend can
// bind the descriptor straight from the JSON.
//
// Annotations uses omitempty so a referrer with no OCI annotations serialises
// without the key rather than as an empty object — the common case for a bare
// signature artifact.
type referrerRecord struct {
	Digest       string            `json:"digest"`
	MediaType    string            `json:"media_type"`
	ArtifactType string            `json:"artifact_type"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// handleListReferrers resolves the tag to its manifest digest, then asks
// registry-core for the OCI referrers pointing at that subject.
func (h *Handler) handleListReferrers(w http.ResponseWriter, r *http.Request) {
	if h.core == nil {
		// Same opt-in gate as handleGetSignature / requireGCAdmin: 404
		// "route disabled" when CORE_GRPC_ADDR is unset so the frontend can
		// hide the Referrers tab instead of surfacing a generic 5xx.
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName, tagName := r.PathValue("org"), r.PathValue("repo"), r.PathValue("tag")

	// Validate every path segment against the CLAUDE.md §7 allowlists before
	// making any gRPC call — never forward an unvalidated string downstream.
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

	// Resolve the repository the same way the manifest + signature routes do —
	// findRepo scopes the lookup to the caller's tenant and returns a generic
	// error (mapped to 404) when the repo is missing or cross-tenant.
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// The tag's current manifest digest is the OCI referrers "subject". This
	// is the identical resolution handleGetManifest / handleGetSignature use.
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	// Bound the outgoing registry-core call with an independent deadline so a
	// stuck core service can't hold the request past listReferrersTimeout.
	ctx, cancel := context.WithTimeout(r.Context(), listReferrersTimeout)
	defer cancel()

	// Repository is the full "<org>/<repo>" OCI name; ArtifactType is left
	// empty to request the unfiltered referrer set (the FE renders every
	// artifact type today).
	resp, err := h.core.ListReferrers(ctx, &corev1.ListReferrersRequest{
		TenantId:      tenantID,
		Repository:    org + "/" + repoName,
		SubjectDigest: tag.GetManifestDigest(),
		ArtifactType:  "",
	})
	if err != nil {
		// Map the gRPC status to an HTTP code the same way the sibling
		// handlers do: NotFound → 404 (subject unknown to core),
		// InvalidArgument → 400 (malformed digest), everything else → 500.
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "manifest not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "invalid referrers request")
				return
			}
		}
		slog.Error("ListReferrers", "err", err, "digest", tag.GetManifestDigest())
		writeError(w, http.StatusInternalServerError, "failed to fetch referrers")
		return
	}

	// Pre-size and guarantee a non-nil slice so the JSON is `[]` (not `null`)
	// for a tag with no referrers — mirrors handleGetManifest / handleGetSignature.
	out := ReferrersResponse{
		Referrers: make([]referrerRecord, 0, len(resp.GetReferrers())),
		Filtered:  resp.GetFiltered(),
	}
	for _, ref := range resp.GetReferrers() {
		out.Referrers = append(out.Referrers, referrerRecord{
			Digest:       ref.GetDigest(),
			MediaType:    ref.GetMediaType(),
			ArtifactType: ref.GetArtifactType(),
			Size:         ref.GetSize(),
			Annotations:  ref.GetAnnotations(),
		})
	}

	writeJSON(w, http.StatusOK, out)
}
