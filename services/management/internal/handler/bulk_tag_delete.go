// Package handler — bulk_tag_delete.go
//
// FE-API-036 — DELETE /api/v1/repositories/{org}/{repo}/tags
//
// Atomicity model: per-tag sub-transaction. Each tag is its own
// `metadata.DeleteTag` gRPC call; one failure does not roll back the
// others. The response reports per-tag success/failure so the UI can
// render "deleted 47/50" and surface which tags failed (e.g. already
// gone, RBAC-blocked at a deeper layer).
//
// The alternative — full atomic delete — would need a new bulk RPC on
// services/metadata and a multi-row DELETE in one transaction. Skipped
// for v1 because (a) the response shape already encodes per-tag results,
// (b) bulk deletes are operator actions where "got 49 of 50, one was
// missing" is acceptable feedback, and (c) it keeps the new surface
// purely additive in services/management with zero proto + metadata
// churn. Document if/when the hot path needs the bulk RPC.
//
// Parallelism: sequential, capped 100 tags per request. Sequential keeps
// the per-tag error mapping deterministic and avoids piling on the
// metadata gRPC pool. 100 tags × ~5ms/call = ~500ms — acceptable for an
// operator-initiated action behind a confirm dialog.
//
// Lives in its own file so concurrent edits to handler.go (RBAC,
// webhooks, admin tenants) don't conflict with the bulk-delete surface.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// bulkDeleteRequest is the JSON body of DELETE …/tags.
type bulkDeleteRequest struct {
	TagNames []string `json:"tag_names"`
}

// bulkDeleteResult is one entry in the response array — present whether
// the per-tag delete succeeded or failed.
type bulkDeleteResult struct {
	TagName string `json:"tag_name"`
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// bulkDeleteResponse is the JSON body returned by the route. Always 200.
type bulkDeleteResponse struct {
	Results []bulkDeleteResult `json:"results"`
}

const maxBulkDeleteTags = 100

func (h *Handler) handleBulkDeleteTags(w http.ResponseWriter, r *http.Request) {
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

	// PENTEST-002: writer or above on THIS REPO (or parent org) may delete
	// tags. Mirrors handleDeleteTag exactly — a bulk action shouldn't gate
	// behind admin when individual deletes only need writer.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var body bulkDeleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(body.TagNames) == 0 {
		writeError(w, http.StatusBadRequest, "tag_names required")
		return
	}

	// Dedupe before counting against the cap so a request with 200 copies
	// of the same tag isn't artificially rejected.
	seen := make(map[string]struct{}, len(body.TagNames))
	deduped := make([]string, 0, len(body.TagNames))
	for _, t := range body.TagNames {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		deduped = append(deduped, t)
	}

	if len(deduped) > maxBulkDeleteTags {
		writeError(w, http.StatusBadRequest, "tag_names exceeds 100")
		return
	}

	// Validate every tag name before any delete fires. A malformed name
	// in the list should fail the whole request — not delete the valid
	// ones first and then 400 — because the caller's intent is unclear.
	for _, t := range deduped {
		if err := validateTagName(t); err != nil {
			writeError(w, http.StatusBadRequest, "invalid tag name: "+t)
			return
		}
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	results := make([]bulkDeleteResult, 0, len(deduped))
	for _, t := range deduped {
		res := bulkDeleteResult{TagName: t}
		if _, err := h.meta.DeleteTag(r.Context(), &metadatav1.DeleteTagRequest{
			RepoId:   repo.GetRepoId(),
			TenantId: tenantID,
			Name:     t,
		}); err != nil {
			res.Reason = bulkDeleteReason(err)
		} else {
			res.Deleted = true
		}
		results = append(results, res)
	}

	writeJSON(w, http.StatusOK, bulkDeleteResponse{Results: results})
}

// bulkDeleteReason maps the gRPC error from DeleteTag into a short, stable
// per-tag failure reason. We deliberately don't leak internal status
// messages — same rule as the rest of the BFF (CLAUDE.md §4.13).
func bulkDeleteReason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		slog.Error("BulkDeleteTag: non-gRPC error", "err", err)
		return "internal error"
	}
	switch st.Code() {
	case codes.NotFound:
		return "tag not found"
	case codes.PermissionDenied:
		return "permission denied"
	case codes.InvalidArgument:
		return "invalid tag name"
	default:
		slog.Error("BulkDeleteTag", "code", st.Code(), "msg", st.Message())
		return "failed"
	}
}
