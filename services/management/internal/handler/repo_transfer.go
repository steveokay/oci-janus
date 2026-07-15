// repo_transfer.go — POST /api/v1/repositories/{org}/{repo}/transfer.
//
// Transferring a repository re-parents it to a different org. Like rename it is
// a two-step operation spanning two services:
//
//  1. registry-metadata.TransferRepository flips the `org_id` column. This is
//     the DURABLE change — storage is repo_id-keyed so no blobs move.
//  2. registry-auth.RewriteRepoRoleScopes migrates the repo-scoped RBAC grants
//     from "oldorg/name" to "neworg/name". Without it every repo-scoped grant
//     would orphan under the old org's namespace.
//
// The authorization bar is higher than rename: the caller must be an admin on
// BOTH the source repo (they're removing it from the source org) AND the
// destination org (they're adding a repo to it). Anything less would let a
// source admin dump repos into an org they have no rights to.
//
// Step 2 is best-effort AFTER the durable transfer, surfaced as `rbac_warning`
// on partial success — same rationale as the rename path (repo_rename.go).
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

// transferRepositoryBody is the JSON request body for the transfer route.
type transferRepositoryBody struct {
	DestOrg string `json:"dest_org"`
}

// transferRepositoryResponse embeds the standard repo shape and adds the two
// transfer-specific fields (identical contract to the rename response).
type transferRepositoryResponse struct {
	RepoResponse
	RolesRewritten int64  `json:"roles_rewritten"`
	RBACWarning    string `json:"rbac_warning,omitempty"`
}

// handleTransferRepository moves a repository to a different org and migrates
// the repo-scoped RBAC grants. Gated on admin of BOTH the source repo and the
// destination org.
func (h *Handler) handleTransferRepository(w http.ResponseWriter, r *http.Request) {
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

	var body transferRepositoryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateOrgName(body.DestOrg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid destination org name")
		return
	}
	// Transferring into the same org is a no-op; reject it early so the
	// operator gets a clear message instead of a metadata AlreadyExists (the
	// repo already lives there under its own name).
	if body.DestOrg == org {
		writeError(w, http.StatusBadRequest, "dest_org must differ from the current org")
		return
	}

	// Dual gate: admin on the source repo AND admin on the destination org.
	// Load the caller's assignments once and check both scopes.
	assignments := h.getUserAssignments(r)
	if !hasScopedRole(assignments, "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions on the source repository")
		return
	}
	if !hasScopedRole(assignments, "org", body.DestOrg, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions on the destination organization")
		return
	}

	// Resolve repo_id — TransferRepository is keyed by ID, not name.
	existing, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Step 1 (durable): re-parent the repo in metadata.
	moved, err := h.meta.TransferRepository(r.Context(), &metadatav1.TransferRepositoryRequest{
		TenantId: tenantID,
		RepoId:   existing.GetRepoId(),
		DestOrg:  body.DestOrg,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.NotFound:
			// The destination org doesn't exist.
			writeError(w, http.StatusNotFound, "destination organization not found")
			return
		case codes.AlreadyExists:
			// The destination org already holds a repo of this name.
			writeError(w, http.StatusConflict, "a repository with that name already exists in the destination organization")
			return
		default:
			slog.Error("TransferRepository", "err", err, "repo_id", existing.GetRepoId())
			writeError(w, http.StatusInternalServerError, "failed to transfer repository")
			return
		}
	}

	// Step 2 (best-effort): migrate repo-scoped RBAC grants from
	// "oldorg/name" to "neworg/name". A failure is reported as a warning
	// rather than rolling back the durable transfer.
	resp := transferRepositoryResponse{RepoResponse: repoToResponse(moved)}
	rewritten, warn := h.rewriteRepoScopesOnTransfer(r.Context(), tenantID, org, body.DestOrg, repoName)
	resp.RolesRewritten = rewritten
	resp.RBACWarning = warn

	writeJSON(w, http.StatusOK, resp)
}

// rewriteRepoScopesOnTransfer calls registry-auth to migrate the repo-scoped
// role_assignments from "oldOrg/name" to "newOrg/name". Returns the number of
// grants rewritten and a non-empty warning when the call failed (the transfer
// has already committed by the time this runs).
func (h *Handler) rewriteRepoScopesOnTransfer(ctx context.Context, tenantID, oldOrg, newOrg, name string) (int64, string) {
	resp, err := h.auth.RewriteRepoRoleScopes(ctx, &authv1.RewriteRepoRoleScopesRequest{
		TenantId: tenantID,
		OldScope: oldOrg + "/" + name,
		NewScope: newOrg + "/" + name,
	})
	if err != nil {
		slog.Warn("RewriteRepoRoleScopes after transfer",
			"err", err, "old_scope", oldOrg+"/"+name, "new_scope", newOrg+"/"+name)
		return 0, "repository was transferred, but migrating its access grants failed — re-grant repo permissions manually: " + err.Error()
	}
	return resp.GetRewritten(), ""
}
