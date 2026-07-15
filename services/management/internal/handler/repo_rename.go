// repo_rename.go — POST /api/v1/repositories/{org}/{repo}/rename.
//
// Renaming a repository is a two-step operation that spans two services:
//
//  1. registry-metadata.RenameRepository flips the `name` column. This is the
//     DURABLE change — the storage layer is repo_id-keyed (manifests/tags
//     reference repo_id; blobs are content-addressed) so no blobs move.
//  2. registry-auth.RewriteRepoRoleScopes migrates the RBAC scope strings.
//     role_assignments in registry-auth key repo-scoped grants on the literal
//     "org/name" string, so without this step every repo-admin/writer/reader
//     grant on the old name would silently orphan.
//
// Step 2 is best-effort AFTER the durable rename, mirroring the FUT-020
// re-sign-on-promote precedent (promote_tag.go): if the scope rewrite fails we
// still return 200 with the renamed repo, but surface an `rbac_warning` so the
// operator knows the grants need a manual re-grant. Failing the whole request
// would be worse — the rename already happened and is not automatically
// rolled back, so a 500 would leave the caller thinking nothing changed.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// renameRepositoryBody is the JSON request body for the rename route.
type renameRepositoryBody struct {
	NewName string `json:"new_name"`
}

// renameRepositoryResponse embeds the standard repo shape and adds two
// rename-specific fields. RolesRewritten is the number of role_assignments
// migrated to the new scope string; RBACWarning is non-empty only when the
// durable rename succeeded but the follow-up scope rewrite failed (partial
// success), so the FE can render a warning banner without a second call.
type renameRepositoryResponse struct {
	RepoResponse
	RolesRewritten int64  `json:"roles_rewritten"`
	RBACWarning    string `json:"rbac_warning,omitempty"`
}

// handleRenameRepository renames a repository within its current org and
// migrates the repo-scoped RBAC grants. Gated on repo admin — the same gate
// the metadata-editing PATCH route uses.
func (h *Handler) handleRenameRepository(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
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

	// Only admins/owners on this repo (or parent org) may rename it — same
	// gate as handleUpdateRepository. Renaming is strictly more disruptive
	// than a metadata edit, so admin is the right floor.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var body renameRepositoryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Validate the target name against the same allowlist new repos use. A
	// clean 400 here beats letting an invalid name reach the gRPC layer.
	if err := validateRepoName(body.NewName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid new repository name")
		return
	}
	// A no-op rename (new == old) is pointless and would trip the metadata
	// UNIQUE(org_id, name) constraint against the repo's own row. Reject it
	// early with a clear message rather than a confusing 409.
	if body.NewName == repoName {
		writeError(w, http.StatusBadRequest, "new_name must differ from the current name")
		return
	}

	// Resolve repo_id — RenameRepository is keyed by ID, not name.
	existing, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Step 1 (durable): flip the name in metadata.
	renamed, err := h.meta.RenameRepository(r.Context(), &metadatav1.RenameRepositoryRequest{
		TenantId: tenantID,
		RepoId:   existing.GetRepoId(),
		NewName:  body.NewName,
	})
	if err != nil {
		// AlreadyExists → a sibling repo in the org already owns the name.
		// Surface it as 409 so the FE can show "name taken" inline.
		if status.Code(err) == codes.AlreadyExists {
			writeError(w, http.StatusConflict, "a repository with that name already exists in this organization")
			return
		}
		slog.Error("RenameRepository", "err", err, "repo_id", existing.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to rename repository")
		return
	}

	// Step 2 (best-effort): migrate repo-scoped RBAC grants from the old
	// "org/oldname" scope string to "org/newname". A failure here does NOT
	// roll back the rename — it is already durable — so we report it as a
	// warning rather than a hard error. See the file header for rationale.
	resp := renameRepositoryResponse{RepoResponse: repoToResponse(renamed)}
	rewritten, warn := h.rewriteRepoScopesOnRename(r.Context(), tenantID, org, repoName, body.NewName)
	resp.RolesRewritten = rewritten
	resp.RBACWarning = warn

	writeJSON(w, http.StatusOK, resp)
}

// rewriteRepoScopesOnRename calls registry-auth to migrate the repo-scoped
// role_assignments from "org/oldName" to "org/newName". It returns the number
// of grants rewritten and a non-empty warning string when the call failed
// (the rename itself has already committed by the time this runs).
func (h *Handler) rewriteRepoScopesOnRename(ctx context.Context, tenantID, org, oldName, newName string) (int64, string) {
	resp, err := h.auth.RewriteRepoRoleScopes(ctx, &authv1.RewriteRepoRoleScopesRequest{
		TenantId: tenantID,
		OldScope: org + "/" + oldName,
		NewScope: org + "/" + newName,
	})
	if err != nil {
		slog.Warn("RewriteRepoRoleScopes after rename",
			"err", err, "old_scope", org+"/"+oldName, "new_scope", org+"/"+newName)
		return 0, "repository was renamed, but migrating its access grants failed — re-grant repo permissions manually: " + err.Error()
	}
	return resp.GetRewritten(), ""
}
