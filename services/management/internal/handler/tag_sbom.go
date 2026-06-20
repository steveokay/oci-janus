// Package handler — per-tag SBOM download (FE-API-033).
//
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/sbom?format=spdx-json
//
// Flow:
//
//	{org}/{repo} → repo_id via h.findRepo (shared resolver)
//	{tag}         → manifest_digest via meta.GetTag
//	meta.GetScanSBOM(tenant_id, manifest_digest)
//	Stream bytes with the matching Content-Type + Content-Disposition.
//
// Auth: any authenticated user with read access to the tenant (same posture
// as FE-API-002 GetManifest and FE-API-014 GetScan). No write side-effects,
// no PII in the body — the SBOM is the same artefact a reader could already
// derive by pulling the image and running syft locally.
//
// CycloneDX (`cyclonedx-json`) is reserved but not yet wired: the scanner
// only emits SPDX 2.3 JSON today. A request for cyclonedx-json returns 400
// with a clear message so callers can fail fast rather than receiving SPDX
// labelled as CycloneDX.
package handler

import (
	"log/slog"
	"net/http"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sbomErrorBody is the JSON wire form for the "no SBOM available" case. The
// `code` field lets the dashboard branch on a stable identifier without
// parsing the human-readable message — `{code: "no-sbom"}` is the contract
// the frontend keys off.
type sbomErrorBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// handleGetTagSBOM streams the SBOM for the manifest currently pointed at by
// the given tag.
//
// The handler must be registered via Handler.Register; it does not register
// itself so all routes live in handler.go for a single source of truth.
func (h *Handler) handleGetTagSBOM(w http.ResponseWriter, r *http.Request) {
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

	// Format parsing — default to spdx-json so a client that just wants "the
	// SBOM" works without thinking about formats. cyclonedx-json is reserved
	// and returns a clear 400 rather than a silent fallback.
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "spdx-json"
	}
	var contentType, extension string
	switch format {
	case "spdx-json":
		contentType = "application/spdx+json"
		extension = ".spdx.json"
	case "cyclonedx-json":
		// CycloneDX is the only other "valid" value, but the scanner doesn't
		// emit it yet (FE-API-033 ships SPDX only — documented in CLAUDE.md).
		// 400 with a precise message so the frontend can disable the option
		// in the dropdown rather than receive SPDX mislabelled as CycloneDX.
		writeError(w, http.StatusBadRequest, "format not yet supported")
		return
	default:
		writeError(w, http.StatusBadRequest, "unsupported format")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Resolve the tag → manifest_digest. The SBOM is stored against the
	// digest, not the tag, so a re-tag of the same digest still returns the
	// same SBOM — and a re-tag onto a fresh digest correctly returns "no
	// SBOM yet" until that new digest is scanned.
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		// Don't leak whether the failure was 404 vs gRPC transport — the
		// caller is told the tag is missing in either case.
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	sbomResp, err := h.meta.GetScanSBOM(r.Context(), &metadatav1.GetScanSBOMRequest{
		TenantId:       tenantID,
		ManifestDigest: tag.GetManifestDigest(),
	})
	if err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.NotFound {
			// Custom body with a stable `code` so the dashboard can render
			// the "Run a scan to generate an SBOM" empty state without
			// pattern-matching on the human-readable string.
			writeJSON(w, http.StatusNotFound, sbomErrorBody{
				Code:  "no-sbom",
				Error: "no SBOM recorded — run a scan to generate one",
			})
			return
		}
		slog.Error("GetScanSBOM", "err", err, "tag", tagName)
		writeError(w, http.StatusInternalServerError, "failed to fetch sbom")
		return
	}

	// Use the digest as the filename so a re-download of "the same SBOM" lands
	// on disk with a stable name regardless of which tag was passed. Strip the
	// "sha256:" prefix so OSes that dislike the colon in filenames still play
	// nice (Windows in particular treats `:` as a stream separator).
	digest := tag.GetManifestDigest()
	if len(digest) > len("sha256:") && digest[:len("sha256:")] == "sha256:" {
		digest = digest[len("sha256:"):]
	}
	filename := digest + extension

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	if _, err := w.Write(sbomResp.GetSbomJson()); err != nil {
		// Headers are already flushed at this point; nothing the client can
		// do but the log line gives operators a breadcrumb.
		slog.Warn("write sbom body", "err", err, "tag", tagName)
	}
}
