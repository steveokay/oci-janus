// Package handler implements the management REST API handlers.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// Handler holds gRPC client dependencies for all management endpoints.
type Handler struct {
	auth authv1.AuthServiceClient
	meta metadatav1.MetadataServiceClient
}

// New creates a Handler wired to the given gRPC clients.
func New(auth authv1.AuthServiceClient, meta metadatav1.MetadataServiceClient) *Handler {
	return &Handler{auth: auth, meta: meta}
}

// Register mounts all management routes onto mux.
// Auth middleware is applied per-route so /healthz remains open.
func (h *Handler) Register(mux *http.ServeMux) {
	authMW := middleware.RequireAuth(h.auth)

	mux.Handle("GET /healthz", http.HandlerFunc(handleHealthz))
	mux.Handle("GET /api/v1/stats", authMW(http.HandlerFunc(h.handleStats)))
	mux.Handle("GET /api/v1/repositories", authMW(http.HandlerFunc(h.handleListRepositories)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleGetRepository)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags", authMW(http.HandlerFunc(h.handleListTags)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/scan", authMW(http.HandlerFunc(h.handleGetScan)))
}

// ---------------------------------------------------------------------------
// /healthz
// ---------------------------------------------------------------------------

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats
// ---------------------------------------------------------------------------

// StatsResponse is the JSON body returned by /api/v1/stats.
type StatsResponse struct {
	TotalRepos         int     `json:"total_repos"`
	StorageUsedBytes   int64   `json:"storage_used_bytes"`
	StorageQuotaBytes  int64   `json:"storage_quota_bytes"`
	DailyPulls         int64   `json:"daily_pulls"`          // TODO: wire pull counter
	VulnerabilityCount int     `json:"vulnerability_count"`  // TODO: aggregate from scan results
	SystemHealthPct    float64 `json:"system_health_pct"`    // TODO: derive from health checks
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	quota, err := h.meta.GetTenantQuotaUsage(r.Context(), &metadatav1.GetTenantQuotaUsageRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("GetTenantQuotaUsage", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch quota")
		return
	}

	// Count repos by draining the stream — replace with a dedicated Count RPC once added.
	stream, err := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
		PageSize: 1000,
	})
	if err != nil {
		slog.Error("ListRepositories for stats", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to count repositories")
		return
	}
	var totalRepos int
	for {
		_, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			break
		}
		totalRepos++
	}

	writeJSON(w, http.StatusOK, StatsResponse{
		TotalRepos:        totalRepos,
		StorageUsedBytes:  quota.GetUsedBytes(),
		StorageQuotaBytes: quota.GetQuotaBytes(),
		DailyPulls:        0,
		SystemHealthPct:   99.9,
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories
// ---------------------------------------------------------------------------

// RepoResponse is the JSON representation of a single repository.
type RepoResponse struct {
	RepoID       string    `json:"repo_id"`
	OrgID        string    `json:"org_id"`
	Name         string    `json:"name"`
	IsPublic     bool      `json:"is_public"`
	StorageUsed  int64     `json:"storage_used_bytes"`
	StorageQuota int64     `json:"storage_quota_bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

func (h *Handler) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	visibility := r.URL.Query().Get("visibility") // "public" | "private" | ""

	pageSize := int32(25)
	if s := r.URL.Query().Get("per_page"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			pageSize = int32(n)
		}
	}

	stream, err := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId:  tenantID,
		PageToken: r.URL.Query().Get("page_token"),
		PageSize:  pageSize,
	})
	if err != nil {
		slog.Error("ListRepositories", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list repositories")
		return
	}

	var repos []RepoResponse
	for {
		repo, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			slog.Error("ListRepositories stream", "err", recvErr)
			break
		}
		if visibility == "public" && !repo.GetIsPublic() {
			continue
		}
		if visibility == "private" && repo.GetIsPublic() {
			continue
		}
		repos = append(repos, repoToResponse(repo))
	}

	if repos == nil {
		repos = []RepoResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repositories": repos,
		"total":        len(repos),
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}
// ---------------------------------------------------------------------------

func (h *Handler) handleGetRepository(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	repoName := r.PathValue("repo")

	repo, err := h.findRepoByName(r, tenantID, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	writeJSON(w, http.StatusOK, repoToResponse(repo))
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags
// ---------------------------------------------------------------------------

// TagResponse is the JSON representation of a single tag.
type TagResponse struct {
	Name           string    `json:"name"`
	ManifestDigest string    `json:"manifest_digest"`
	UpdatedAt      time.Time `json:"updated_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func (h *Handler) handleListTags(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	repoName := r.PathValue("repo")

	repo, err := h.findRepoByName(r, tenantID, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	stream, err := h.meta.ListTags(r.Context(), &metadatav1.ListTagsRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		PageSize: 100,
	})
	if err != nil {
		slog.Error("ListTags", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list tags")
		return
	}

	var tags []TagResponse
	for {
		tag, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			slog.Error("ListTags stream", "err", recvErr)
			break
		}
		tags = append(tags, TagResponse{
			Name:           tag.GetName(),
			ManifestDigest: tag.GetManifestDigest(),
			UpdatedAt:      tag.GetUpdatedAt().AsTime(),
			CreatedAt:      tag.GetCreatedAt().AsTime(),
		})
	}

	if tags == nil {
		tags = []TagResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/scan
// ---------------------------------------------------------------------------

// ScanResponse is the JSON representation of the latest scan result.
type ScanResponse struct {
	ScanID          string           `json:"scan_id"`
	Status          string           `json:"status"`
	ScannerName     string           `json:"scanner_name"`
	ScannerVersion  string           `json:"scanner_version"`
	SeverityCounts  map[string]int32 `json:"severity_counts"`
	FindingsJSON    []byte           `json:"findings_json,omitempty"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
}

func (h *Handler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	repoName := r.PathValue("repo")

	repo, err := h.findRepoByName(r, tenantID, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	result, err := h.meta.GetScanResult(r.Context(), &metadatav1.GetScanResultRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		slog.Error("GetScanResult", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch scan result")
		return
	}

	writeJSON(w, http.StatusOK, ScanResponse{
		ScanID:         result.GetScanId(),
		Status:         result.GetStatus(),
		ScannerName:    result.GetScannerName(),
		ScannerVersion: result.GetScannerVersion(),
		SeverityCounts: result.GetSeverityCounts(),
		FindingsJSON:   result.GetFindingsJson(),
		StartedAt:      result.GetStartedAt().AsTime(),
		CompletedAt:    result.GetCompletedAt().AsTime(),
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// findRepoByName scans ListRepositories for the first repo matching name.
// TODO: replace with a direct GetRepositoryByName gRPC call once added to the
// MetadataService proto — scanning the full list is O(n) and not suitable for
// tenants with hundreds of repositories.
func (h *Handler) findRepoByName(r *http.Request, tenantID, name string) (*metadatav1.Repository, error) {
	stream, err := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
		PageSize: 1000,
	})
	if err != nil {
		return nil, err
	}
	for {
		repo, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if repo.GetName() == name {
			return repo, nil
		}
	}
	return nil, fmt.Errorf("repository %q not found for tenant %s", name, tenantID)
}

func repoToResponse(r *metadatav1.Repository) RepoResponse {
	return RepoResponse{
		RepoID:       r.GetRepoId(),
		OrgID:        r.GetOrgId(),
		Name:         r.GetName(),
		IsPublic:     r.GetIsPublic(),
		StorageUsed:  r.GetStorageUsed(),
		StorageQuota: r.GetStorageQuota(),
		CreatedAt:    r.GetCreatedAt().AsTime(),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
