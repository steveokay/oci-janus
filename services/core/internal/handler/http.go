// Package handler implements the OCI Distribution Spec v1.1 HTTP API.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveokay/oci-janus/services/core/internal/service"
)

const (
	mediaTypeDockerManifestV2 = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeOCIManifest      = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex         = "application/vnd.oci.image.index.v1+json"
	maxManifestBodyBytes      = 4 * 1024 * 1024 // 4 MiB
)

var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Handler holds all dependencies for the OCI HTTP API.
type Handler struct {
	auth      *service.AuthClient
	registry  *service.Registry
	authRealm string
}

// New constructs a Handler.
func New(auth *service.AuthClient, registry *service.Registry, authRealm string) *Handler {
	return &Handler{auth: auth, registry: registry, authRealm: authRealm}
}

// Register mounts all OCI /v2/ routes onto mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/v2/", h.dispatchOCI)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// dispatchOCI routes all /v2/ requests by path structure.
// All OCI paths follow: /v2/<name>/(<operation>) where <name> = org/repo.
func (h *Handler) dispatchOCI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	path = strings.Trim(path, "/")

	// GET /v2/ — version check
	if path == "" {
		h.handleVersionCheck(w, r)
		return
	}

	segments := strings.Split(path, "/")
	n := len(segments)

	switch {
	// /v2/<name>/tags/list — n>=4
	case n >= 4 && segments[n-2] == "tags" && segments[n-1] == "list":
		name := strings.Join(segments[:n-2], "/")
		h.handleTagsList(w, r, name)

	// /v2/<name>/blobs/uploads/<uuid> — n>=5
	case n >= 5 && segments[n-3] == "blobs" && segments[n-2] == "uploads":
		name := strings.Join(segments[:n-3], "/")
		uploadUUID := segments[n-1]
		switch r.Method {
		case http.MethodGet:
			h.handleGetUpload(w, r, name, uploadUUID)
		case http.MethodPatch:
			h.handlePatchUpload(w, r, name, uploadUUID)
		case http.MethodPut:
			h.handleCompleteUpload(w, r, name, uploadUUID)
		case http.MethodDelete:
			h.handleCancelUpload(w, r, name, uploadUUID)
		default:
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	// /v2/<name>/blobs/uploads  (POST — trailing slash stripped) — n>=4
	case n >= 4 && segments[n-2] == "blobs" && segments[n-1] == "uploads":
		name := strings.Join(segments[:n-2], "/")
		if r.Method != http.MethodPost {
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
			return
		}
		h.handleInitiateUpload(w, r, name)

	// /v2/<name>/manifests/<reference> — n>=4
	case n >= 4 && segments[n-2] == "manifests":
		name := strings.Join(segments[:n-2], "/")
		ref := segments[n-1]
		switch r.Method {
		case http.MethodGet:
			h.handleGetManifest(w, r, name, ref)
		case http.MethodHead:
			h.handleHeadManifest(w, r, name, ref)
		case http.MethodPut:
			h.handlePutManifest(w, r, name, ref)
		case http.MethodDelete:
			h.handleDeleteManifest(w, r, name, ref)
		default:
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	// /v2/<name>/blobs/<digest> — n>=4
	case n >= 4 && segments[n-2] == "blobs":
		name := strings.Join(segments[:n-2], "/")
		digest := segments[n-1]
		switch r.Method {
		case http.MethodGet:
			h.handleGetBlob(w, r, name, digest)
		case http.MethodHead:
			h.handleHeadBlob(w, r, name, digest)
		case http.MethodDelete:
			h.handleDeleteBlob(w, r, name, digest)
		default:
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	default:
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
	}
}

// --- Auth helpers ---

func (h *Handler) authenticate(r *http.Request) (*service.TokenClaims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, service.ErrUnauthorized
	}
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		return h.auth.ValidateBearer(r.Context(), token)
	}
	if strings.HasPrefix(authHeader, "Basic ") {
		encoded := strings.TrimPrefix(authHeader, "Basic ")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			// try without padding
			decoded, err = base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, service.ErrUnauthorized
			}
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, service.ErrUnauthorized
		}
		return h.auth.ValidateAPIKey(r.Context(), parts[0], parts[1])
	}
	return nil, service.ErrUnauthorized
}

// --- Version check ---

func (h *Handler) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	_, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (h *Handler) challengeAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry-core"`, h.authRealm))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	writeErrors(w, ociErr{Code: "UNAUTHORIZED", Message: "authentication required"})
}

// --- Tags ---

func (h *Handler) handleTagsList(w http.ResponseWriter, r *http.Request, name string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if err := service.ValidateName(name); err != nil {
		ociError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w)
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		slog.ErrorContext(r.Context(), "get repository", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	n := int32(100)
	if ns := r.URL.Query().Get("n"); ns != "" {
		if v, err := strconv.ParseInt(ns, 10, 32); err == nil && v > 0 {
			n = int32(v)
		}
	}
	last := r.URL.Query().Get("last")

	tags, err := h.registry.ListTags(r.Context(), tenantID, repo.GetRepoId(), n, last)
	if err != nil {
		slog.ErrorContext(r.Context(), "list tags", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	if int32(len(tags)) == n && n > 0 {
		nextLast := tags[len(tags)-1]
		link := fmt.Sprintf(`</v2/%s/tags/list?last=%s&n=%d>; rel="next"`, name, nextLast, n)
		w.Header().Set("Link", link)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": name,
		"tags": tags,
	})
}

// --- Manifests ---

func (h *Handler) handleGetManifest(w http.ResponseWriter, r *http.Request, name, reference string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w)
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	manifest, err := h.registry.GetManifest(r.Context(), tenantID, repo.GetRepoId(), reference)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Content-Type", manifest.GetMediaType())
	w.Header().Set("Docker-Content-Digest", manifest.GetDigest())
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(manifest.GetRawJson())), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(manifest.GetRawJson())
}

func (h *Handler) handleHeadManifest(w http.ResponseWriter, r *http.Request, name, reference string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w)
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	manifest, err := h.registry.GetManifest(r.Context(), tenantID, repo.GetRepoId(), reference)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Content-Type", manifest.GetMediaType())
	w.Header().Set("Docker-Content-Digest", manifest.GetDigest())
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(manifest.GetRawJson())), 10))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handlePutManifest(w http.ResponseWriter, r *http.Request, name, reference string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}
	if err := service.ValidateName(name); err != nil {
		ociError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}

	mediaType := r.Header.Get("Content-Type")
	if !isAllowedManifestMediaType(mediaType) {
		ociError(w, http.StatusBadRequest, "MANIFEST_INVALID", "unsupported media type")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxManifestBodyBytes))
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "failed to read body")
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	digest, err := h.registry.PutManifest(
		r.Context(), tenantID, repo.GetRepoId(), name, reference, mediaType, body, claims.UserID,
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "put manifest", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digest))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleDeleteManifest(w http.ResponseWriter, r *http.Request, name, reference string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "delete") {
		h.challengeAuth(w)
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	if err := h.registry.DeleteManifest(r.Context(), tenantID, repo.GetRepoId(), reference); err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	} else if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// --- Blobs ---

func (h *Handler) handleHeadBlob(w http.ResponseWriter, r *http.Request, name, digest string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w)
		return
	}
	if !digestRE.MatchString(digest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	exists, size, err := h.registry.BlobExists(r.Context(), claims.TenantID, digest)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}
	if !exists {
		ociError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleGetBlob(w http.ResponseWriter, r *http.Request, name, digest string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w)
		return
	}
	if !digestRE.MatchString(digest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	exists, size, err := h.registry.BlobExists(r.Context(), claims.TenantID, digest)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}
	if !exists {
		ociError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	if _, err := h.registry.GetBlob(r.Context(), claims.TenantID, digest, w); err != nil {
		slog.ErrorContext(r.Context(), "stream blob", "err", err)
	}
}

func (h *Handler) handleDeleteBlob(w http.ResponseWriter, r *http.Request, name, digest string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "delete") {
		h.challengeAuth(w)
		return
	}
	if !digestRE.MatchString(digest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	tenantID := claims.TenantID
	repo, err := h.registry.GetOrCreateRepository(r.Context(), tenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	if err := h.registry.DeleteBlob(r.Context(), tenantID, repo.GetRepoId(), digest); err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
		return
	} else if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// --- Uploads ---

func (h *Handler) handleInitiateUpload(w http.ResponseWriter, r *http.Request, name string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}
	if err := service.ValidateName(name); err != nil {
		ociError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}

	// cross-repo blob mount: ?from=<repo>&mount=<digest>
	mountDigest := r.URL.Query().Get("mount")
	if mountDigest != "" && digestRE.MatchString(mountDigest) {
		exists, _, _ := h.registry.BlobExists(r.Context(), claims.TenantID, mountDigest)
		if exists {
			w.Header().Set("Docker-Content-Digest", mountDigest)
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, mountDigest))
			w.WriteHeader(http.StatusCreated)
			return
		}
	}

	uploadUUID, err := h.registry.InitiateUpload(r.Context(), claims.TenantID, name)
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) handleGetUpload(w http.ResponseWriter, r *http.Request, name, uploadUUID string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}

	st, err := h.registry.GetUpload(r.Context(), uploadUUID)
	if err == service.ErrUploadNotFound {
		ociError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}
	if err != nil {
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", fmt.Sprintf("0-%d", st.Offset))
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handlePatchUpload(w http.ResponseWriter, r *http.Request, name, uploadUUID string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}

	_, err = h.registry.AppendChunk(r.Context(), uploadUUID, r.Body, r.ContentLength)
	if err == service.ErrUploadNotFound {
		ociError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "append chunk", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	st, _ := h.registry.GetUpload(r.Context(), uploadUUID)
	var offset int64
	if st != nil {
		offset = st.Offset
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", fmt.Sprintf("0-%d", offset-1))
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) handleCompleteUpload(w http.ResponseWriter, r *http.Request, name, uploadUUID string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}

	expectedDigest := r.URL.Query().Get("digest")
	if expectedDigest != "" && !digestRE.MatchString(expectedDigest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest parameter")
		return
	}

	digest, _, err := h.registry.CompleteUpload(r.Context(), uploadUUID, expectedDigest, r.Body, r.ContentLength)
	if err == service.ErrUploadNotFound {
		ociError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload not found")
		return
	}
	if err == service.ErrDigestMismatch {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest mismatch")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "complete upload", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleCancelUpload(w http.ResponseWriter, r *http.Request, name, uploadUUID string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w)
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w)
		return
	}

	_ = h.registry.CancelUpload(r.Context(), uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

func isAllowedManifestMediaType(mt string) bool {
	switch strings.SplitN(mt, ";", 2)[0] {
	case mediaTypeDockerManifestV2, mediaTypeOCIManifest, mediaTypeOCIIndex:
		return true
	}
	return false
}

type ociErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ociError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	writeErrors(w, ociErr{Code: code, Message: message})
}

func writeErrors(w http.ResponseWriter, errs ...ociErr) {
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs})
}
