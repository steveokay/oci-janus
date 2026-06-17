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

// responseWriter wraps http.ResponseWriter to capture the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

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
	rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	w = rw
	defer func() {
		slog.InfoContext(r.Context(), "oci request",
			"method", r.Method, "path", r.URL.Path, "status", rw.status)
	}()
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
	// /v2/<name>/tags/list — n>=3 (name may be single-segment for cross-repo operations)
	case n >= 3 && segments[n-2] == "tags" && segments[n-1] == "list":
		name := strings.Join(segments[:n-2], "/")
		h.handleTagsList(w, r, name)

	// /v2/<name>/blobs/uploads/<uuid> — n>=4
	case n >= 4 && segments[n-3] == "blobs" && segments[n-2] == "uploads":
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

	// /v2/<name>/blobs/uploads  (POST — trailing slash stripped) — n>=3
	case n >= 3 && segments[n-2] == "blobs" && segments[n-1] == "uploads":
		name := strings.Join(segments[:n-2], "/")
		if r.Method != http.MethodPost {
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
			return
		}
		h.handleInitiateUpload(w, r, name)

	// /v2/<name>/manifests/<reference> — n>=3
	case n >= 3 && segments[n-2] == "manifests":
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

	// /v2/<name>/blobs/<digest> — n>=3
	case n >= 3 && segments[n-2] == "blobs":
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

	// /v2/<name>/referrers/<digest> — OCI referrers API — n>=3
	case n >= 3 && segments[n-2] == "referrers":
		name := strings.Join(segments[:n-2], "/")
		digest := segments[n-1]
		if r.Method == http.MethodGet {
			h.handleReferrers(w, r, name, digest)
		} else {
			ociError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		}

	default:
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
	}
}

// --- Auth helpers ---

// checkAccess enforces RBAC for the given repository and action ("push" or "pull").
//
// It calls GetUserPermissions on registry-auth to retrieve the caller's RBAC access
// list and checks that at least one entry covers the requested repository+action.
// This is called in addition to the JWT token's own access list: the JWT records what
// the client *requested* at token-issuance time, while RBAC records what the user is
// *allowed* to do at the server — these must both agree for access to proceed.
//
// Returns nil when access is permitted, or an error suitable for returning 403.
func (h *Handler) checkAccess(r *http.Request, claims *service.TokenClaims, repoName, action string) error {
	perms, err := h.auth.GetUserPermissions(r.Context(), claims.UserID, claims.TenantID)
	if err != nil {
		// GetUserPermissions failing must not grant access — fail closed.
		slog.ErrorContext(r.Context(), "get user permissions failed", "err", err)
		return service.ErrForbidden
	}
	// Walk the returned access list; accept if any entry matches the repo and action.
	// Three name patterns are accepted:
	//   "*"        — global wildcard, matches any repo
	//   "org/*"    — org-level wildcard (from org-scoped role assignments), matches any repo in that org
	//   "org/repo" — exact repo match
	for _, a := range perms {
		name := a.GetName()
		matched := name == "*" || name == repoName
		if !matched && strings.HasSuffix(name, "/*") {
			// "org/*" covers all repos whose name starts with "org/"
			orgPrefix := strings.TrimSuffix(name, "*")
			matched = strings.HasPrefix(repoName, orgPrefix)
		}
		if !matched {
			continue
		}
		for _, act := range a.GetActions() {
			if act == action || act == "*" {
				return nil
			}
		}
	}
	return service.ErrForbidden
}

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
		h.challengeAuth(w, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

// challengeAuth sends a 401 Bearer challenge. scope should be
// "repository:<name>:<actions>" for resource endpoints, or "" for /v2/.
func (h *Handler) challengeAuth(w http.ResponseWriter, scope string) {
	challenge := fmt.Sprintf(`Bearer realm=%q,service="registry-core"`, h.authRealm)
	if scope != "" {
		challenge += fmt.Sprintf(`,scope=%q`, scope)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	writeErrors(w, ociErr{Code: "UNAUTHORIZED", Message: "authentication required"})
}

// --- Tags ---

func (h *Handler) handleTagsList(w http.ResponseWriter, r *http.Request, name string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if err := service.ValidateName(name); err != nil {
		ociError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	// Enforce RBAC: verify the user holds at least reader access for pull operations.
	if err := h.checkAccess(r, claims, name, "pull"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}

	tenantID := claims.TenantID
	// Read path: never create. Return 404 if the repository does not exist.
	repo, err := h.registry.GetRepository(r.Context(), tenantID, name)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}
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
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	// Enforce RBAC: verify the user holds at least reader access for pull operations.
	if err := h.checkAccess(r, claims, name, "pull"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}

	tenantID := claims.TenantID
	// Read path: never create. Return 404 if the repository does not exist.
	repo, err := h.registry.GetRepository(r.Context(), tenantID, name)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}
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
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	// Enforce RBAC: verify the user holds at least reader access for pull operations.
	if err := h.checkAccess(r, claims, name, "pull"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}

	tenantID := claims.TenantID
	// Read path: never create. Return 404 if the repository does not exist.
	repo, err := h.registry.GetRepository(r.Context(), tenantID, name)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}
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
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if err := service.ValidateName(name); err != nil {
		ociError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}
	// Enforce RBAC: verify the user holds at least writer access for push operations.
	if err := h.checkAccess(r, claims, name, "push"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
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

	digest, subjectDigest, err := h.registry.PutManifest(
		r.Context(), tenantID, repo.GetRepoId(), name, reference, mediaType, body, claims.UserID,
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "put manifest", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digest))
	// OCI-Subject signals to clients that this manifest is a referrer; required by OCI spec §4.5.
	if subjectDigest != "" {
		w.Header().Set("OCI-Subject", subjectDigest)
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) handleDeleteManifest(w http.ResponseWriter, r *http.Request, name, reference string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w, "repository:"+name+":delete")
		return
	}
	if !claims.HasAction(name, "delete") {
		h.challengeAuth(w, "repository:"+name+":delete")
		return
	}
	// Enforce RBAC: delete is a write operation; require at least writer role.
	if err := h.checkAccess(r, claims, name, "push"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}

	tenantID := claims.TenantID
	// Delete path: never create. Return 404 if the repository does not exist.
	repo, err := h.registry.GetRepository(r.Context(), tenantID, name)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}
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
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	// Enforce RBAC: verify the user holds at least reader access for pull operations.
	if err := h.checkAccess(r, claims, name, "pull"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
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
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	// Enforce RBAC: verify the user holds at least reader access for pull operations.
	if err := h.checkAccess(r, claims, name, "pull"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
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
		h.challengeAuth(w, "repository:"+name+":delete")
		return
	}
	if !claims.HasAction(name, "delete") {
		h.challengeAuth(w, "repository:"+name+":delete")
		return
	}
	// Enforce RBAC: delete is a write operation; require at least writer role.
	if err := h.checkAccess(r, claims, name, "push"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}
	if !digestRE.MatchString(digest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	tenantID := claims.TenantID
	// Delete path: never create. Return 404 if the repository does not exist.
	repo, err := h.registry.GetRepository(r.Context(), tenantID, name)
	if err == service.ErrNotFound {
		ociError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}
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
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	// Enforce RBAC: verify the user holds at least writer access before starting an upload.
	if err := h.checkAccess(r, claims, name, "push"); err != nil {
		ociError(w, http.StatusForbidden, "DENIED", "access denied")
		return
	}

	// Cross-repo blob mount: ?mount=<digest>&from=<repo>
	// Per OCI spec §4.6: both parameters MUST be present to attempt mount;
	// if only ?mount is given, the registry MUST begin a regular upload (202).
	mountDigest := r.URL.Query().Get("mount")
	fromRepo := r.URL.Query().Get("from")
	if mountDigest != "" && fromRepo != "" && digestRE.MatchString(mountDigest) {
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
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
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

	rangeEnd := st.Offset - 1
	if rangeEnd < 0 {
		rangeEnd = 0
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadUUID))
	w.Header().Set("Range", fmt.Sprintf("0-%d", rangeEnd))
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handlePatchUpload(w http.ResponseWriter, r *http.Request, name, uploadUUID string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}

	// Validate Content-Range if present — OCI spec requires 416 when start != current offset.
	if cr := r.Header.Get("Content-Range"); cr != "" {
		start, _, ok := parseContentRange(cr)
		if !ok {
			ociError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "invalid Content-Range header")
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
		if start != st.Offset {
			ociError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "content range start does not match current offset")
			return
		}
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
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
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
		h.challengeAuth(w, "repository:"+name+":push")
		return
	}
	if !claims.HasAction(name, "push") {
		h.challengeAuth(w, "repository:"+name+":push")
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

// handleReferrers implements GET /v2/<name>/referrers/<digest> per OCI Distribution Spec v1.1 §4.5.
// It returns an OCI image index containing all manifests that reference the given subject digest.
// An optional ?artifactType= query parameter filters the results; when filtering is applied,
// OCI-Filters-Applied: artifactType is set in the response.
func (h *Handler) handleReferrers(w http.ResponseWriter, r *http.Request, name, digest string) {
	claims, err := h.authenticate(r)
	if err != nil {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !claims.HasAction(name, "pull") {
		h.challengeAuth(w, "repository:"+name+":pull")
		return
	}
	if !digestRE.MatchString(digest) {
		ociError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	artifactType := r.URL.Query().Get("artifactType")

	descs, filtered, err := h.registry.GetReferrers(r.Context(), claims.TenantID, name, digest, artifactType)
	if err != nil {
		slog.ErrorContext(r.Context(), "get referrers", "err", err)
		ociError(w, http.StatusInternalServerError, "UNKNOWN", "internal error")
		return
	}

	// OCI spec §4.5.1: manifests field must be a non-null array (empty if no referrers).
	if descs == nil {
		descs = []service.ReferrerDescriptor{}
	}

	resp := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     descs,
	}

	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	// Only set OCI-Filters-Applied when we actually applied a filter (OCI spec §4.5.1).
	if filtered {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// parseContentRange parses an HTTP Content-Range header.
// Supports both OCI Distribution Spec format ("0-20") and RFC 7233 format
// ("bytes 0-20/21" or "bytes 0-20/*"). Returns (start, end, true) on success.
func parseContentRange(s string) (start, end int64, ok bool) {
	// Strip optional RFC 7233 "bytes " unit prefix.
	s = strings.TrimPrefix(s, "bytes ")
	// Strip optional "/<total>" suffix.
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	idx := strings.IndexByte(s, '-')
	if idx < 0 {
		return 0, 0, false
	}
	var errS, errE error
	start, errS = strconv.ParseInt(s[:idx], 10, 64)
	end, errE = strconv.ParseInt(s[idx+1:], 10, 64)
	if errS != nil || errE != nil {
		return 0, 0, false
	}
	return start, end, true
}
