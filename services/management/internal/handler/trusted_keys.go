// Package handler — trusted_keys.go
//
// Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist.
//
// Three routes that wrap the metadata trusted-key RPCs behind a repo-
// admin gate (write paths) or a repo-reader gate (read path):
//
//   GET    /api/v1/repositories/{org}/{repo}/trusted-keys             — list
//   POST   /api/v1/repositories/{org}/{repo}/trusted-keys             — add  (body: {key_id, display_name})
//   DELETE /api/v1/repositories/{org}/{repo}/trusted-keys/{key_id}    — remove
//
// The allowlist itself isn't sensitive (it's the set of public-key
// identifiers the operator chose to trust — anyone with reader access
// already sees signed images and their key_ids on the tag detail
// page), so List is reader-accessible. Add/Remove are security
// transitions that gate pull admission, so they require repo admin
// like the Settings tab's other toggles.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// trustedKeyResponse is the JSON wire form of one allowed key. Mirrors
// the proto RepositoryTrustedKey 1:1 except `added_at` is ISO-8601
// rather than the proto Timestamp shape, matching the rest of the BFF
// responses.
type trustedKeyResponse struct {
	ID          string `json:"id"`
	KeyID       string `json:"key_id"`
	DisplayName string `json:"display_name,omitempty"`
	AddedBy     string `json:"added_by,omitempty"`
	AddedAt     string `json:"added_at"`
}

// addTrustedKeyBody is the expected JSON body for POST. key_id is
// required; display_name is optional but strongly encouraged so the
// dashboard table renders meaningful labels.
type addTrustedKeyBody struct {
	KeyID       string `json:"key_id"`
	DisplayName string `json:"display_name"`
}

// keyIDPattern matches the shape services/signer emits today: 16 hex
// chars for the Vault Transit backend (truncated SHA256). Cosign
// keyless will use full SHA256 fingerprints (64 hex). Allow both
// (8-128 chars of hex or colon-separated hex like `sha256:...`).
// Loose enough to accept future formats, strict enough to reject the
// most common copy-paste mistakes (spaces, quotes, full PEM blocks).
var keyIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_:./-]{8,256}$`)

func (h *Handler) handleListTrustedKeys(w http.ResponseWriter, r *http.Request) {
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

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	resp, err := h.meta.ListRepositoryTrustedKeys(r.Context(), &metadatav1.ListRepositoryTrustedKeysRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		slog.Error("ListRepositoryTrustedKeys", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to list trusted keys")
		return
	}

	out := make([]trustedKeyResponse, 0, len(resp.GetKeys()))
	for _, k := range resp.GetKeys() {
		out = append(out, trustedKeyResponse{
			ID:          k.GetId(),
			KeyID:       k.GetKeyId(),
			DisplayName: k.GetDisplayName(),
			AddedBy:     k.GetAddedBy(),
			AddedAt:     k.GetAddedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *Handler) handleAddTrustedKey(w http.ResponseWriter, r *http.Request) {
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

	// Repo admin gate — security-relevant transition: adding a key
	// either widens (from "nothing trusted") or, more commonly,
	// changes the set of who can sign for this repo. Mirrors the
	// posture of the Settings-tab toggles.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body addTrustedKeyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !keyIDPattern.MatchString(body.KeyID) {
		writeError(w, http.StatusBadRequest, "invalid key_id (expected 8-256 chars of hex / base64-ish identifier)")
		return
	}
	if len(body.DisplayName) > 128 {
		writeError(w, http.StatusBadRequest, "display_name too long (max 128 chars)")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// added_by sources from the actor user_id injected by the auth
	// middleware so the audit chain stays intact. Empty string for
	// principals without a user_id (system-driven callers); the SQL
	// turns "" into NULL via NULLIF so the column stays type-clean.
	addedBy := middleware.UserIDFromContext(r.Context())

	k, err := h.meta.AddRepositoryTrustedKey(r.Context(), &metadatav1.AddRepositoryTrustedKeyRequest{
		TenantId:    tenantID,
		RepoId:      repo.GetRepoId(),
		KeyId:       body.KeyID,
		DisplayName: body.DisplayName,
		AddedBy:     addedBy,
	})
	if err != nil {
		slog.Error("AddRepositoryTrustedKey", "err", err, "repo", org+"/"+repoName, "key_id", body.KeyID)
		writeError(w, http.StatusInternalServerError, "failed to add trusted key")
		return
	}

	writeJSON(w, http.StatusCreated, trustedKeyResponse{
		ID:          k.GetId(),
		KeyID:       k.GetKeyId(),
		DisplayName: k.GetDisplayName(),
		AddedBy:     k.GetAddedBy(),
		AddedAt:     k.GetAddedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *Handler) handleRemoveTrustedKey(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName, keyID := r.PathValue("org"), r.PathValue("repo"), r.PathValue("key_id")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if !keyIDPattern.MatchString(keyID) {
		writeError(w, http.StatusBadRequest, "invalid key_id")
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

	if _, err := h.meta.RemoveRepositoryTrustedKey(r.Context(), &metadatav1.RemoveRepositoryTrustedKeyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		KeyId:    keyID,
	}); err != nil {
		// metadata's repository returns ErrNotFound when the key isn't
		// approved; mapErr surfaces that as codes.NotFound which we
		// translate to a clean 404 here so the FE can distinguish
		// "already removed" from a real failure.
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "trusted key not found")
			return
		}
		if errors.Is(err, context.Canceled) {
			// Client gave up mid-flight — no point logging this loudly.
			return
		}
		slog.Error("RemoveRepositoryTrustedKey", "err", err, "repo", org+"/"+repoName, "key_id", keyID)
		writeError(w, http.StatusInternalServerError, "failed to remove trusted key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
