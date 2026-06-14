// Package handler implements the management REST API handlers.
//
// Every handler extracts tenant_id from the request context (injected by
// RequireAuth middleware) and passes it to every gRPC call. Tenant ID is
// never accepted from user-supplied headers or body fields — this enforces
// the isolation guarantee from CLAUDE.md §9.
//
// Handler methods are grouped by resource in the following order:
//   /healthz → stats → repositories (CRUD) → tags (list/delete)
//   → scan (get/trigger) → builds (list)
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// maxBodyBytes caps incoming JSON request bodies to prevent large-payload attacks.
const maxBodyBytes = 4096

// Handler holds gRPC client dependencies for all management endpoints.
type Handler struct {
	auth  authv1.AuthServiceClient
	meta  metadatav1.MetadataServiceClient
	audit auditv1.AuditServiceClient
	// pub publishes events to the registry.events RabbitMQ exchange.
	pub *publisher.Publisher
}

// New creates a Handler wired to the given gRPC clients and RabbitMQ publisher.
func New(
	auth authv1.AuthServiceClient,
	meta metadatav1.MetadataServiceClient,
	audit auditv1.AuditServiceClient,
	pub *publisher.Publisher,
) *Handler {
	return &Handler{auth: auth, meta: meta, audit: audit, pub: pub}
}

// Register mounts all management routes onto mux.
//
// /healthz is open (no auth). Every other route is wrapped with RequireAuth,
// which validates the Bearer token via registry-auth gRPC and stores the
// tenant_id in the request context.
//
// Route patterns use Go 1.22+ net/http syntax: {param} captures a single
// path segment, {param...} captures the remainder.
func (h *Handler) Register(mux *http.ServeMux) {
	authMW := middleware.RequireAuth(h.auth)

	// Health — unauthenticated, used by docker-compose and K8s probes.
	mux.Handle("GET /healthz", http.HandlerFunc(handleHealthz))

	// Tenant-scoped aggregate stats.
	mux.Handle("GET /api/v1/stats", authMW(http.HandlerFunc(h.handleStats)))

	// Repository management.
	mux.Handle("GET /api/v1/repositories", authMW(http.HandlerFunc(h.handleListRepositories)))
	mux.Handle("POST /api/v1/repositories", authMW(http.HandlerFunc(h.handleCreateRepository)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleGetRepository)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleDeleteRepository)))

	// Tag management.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags", authMW(http.HandlerFunc(h.handleListTags)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}", authMW(http.HandlerFunc(h.handleDeleteTag)))

	// Vulnerability scanning — tag-scoped per CLAUDE.md §4.13.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleGetScan)))
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleTriggerScan)))

	// Build / audit history — returns empty list until registry-audit query API is ready.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds", authMW(http.HandlerFunc(h.handleListBuilds)))
}

// ---------------------------------------------------------------------------
// GET /healthz
// ---------------------------------------------------------------------------

// handleHealthz returns a 200 JSON body. Used by Compose healthcheck and K8s
// probes. Intentionally unauthenticated — no tenant information is disclosed.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats
// ---------------------------------------------------------------------------

// StatsResponse is the JSON body returned by GET /api/v1/stats.
type StatsResponse struct {
	TotalRepos         int     `json:"total_repos"`
	StorageUsedBytes   int64   `json:"storage_used_bytes"`
	StorageQuotaBytes  int64   `json:"storage_quota_bytes"`
	DailyPulls         int64   `json:"daily_pulls"`         // TODO: wire pull counter from registry-audit
	VulnerabilityCount int     `json:"vulnerability_count"` // TODO: aggregate from scan_results table
	SystemHealthPct    float64 `json:"system_health_pct"`   // TODO: derive from gRPC health checks
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

	// Count repos by draining ListRepositories stream.
	// TODO: replace with a dedicated CountRepositories RPC to avoid O(n) drain.
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
			slog.Error("ListRepositories stream for stats", "err", recvErr)
			writeError(w, http.StatusInternalServerError, "failed to count repositories")
			return
		}
		totalRepos++
	}

	vulns, err := h.meta.GetTenantVulnerabilityCount(r.Context(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("GetTenantVulnerabilityCount", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch vulnerability counts")
		return
	}

	writeJSON(w, http.StatusOK, StatsResponse{
		TotalRepos:         totalRepos,
		StorageUsedBytes:   quota.GetUsedBytes(),
		StorageQuotaBytes:  quota.GetQuotaBytes(),
		DailyPulls:         0,
		VulnerabilityCount: int(vulns.GetTotal()),
		SystemHealthPct:    99.9,
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

	// Optional filter: "public" | "private" | "" (all).
	visibility := r.URL.Query().Get("visibility")

	// per_page: 1–100, default 25.
	pageSize := int32(25)
	if s := r.URL.Query().Get("per_page"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			pageSize = int32(n)
		}
	}

	// Validate page_token before forwarding — any user-supplied string must be
	// checked against an allowlist before it reaches a downstream service (CLAUDE.md §7).
	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	stream, err := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId:  tenantID,
		PageToken: pageToken,
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
		// Apply client-side visibility filter — metadata streams all repos.
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
// POST /api/v1/repositories
// ---------------------------------------------------------------------------

// createRepositoryBody is the expected JSON body for repository creation.
type createRepositoryBody struct {
	// Org is the organisation namespace for the new repository (e.g. "myorg").
	Org string `json:"org"`
	// Name is the short repository name within the org (e.g. "myimage").
	// The full name stored in metadata is "org/name".
	Name string `json:"name"`
	// IsPublic controls whether the repository is publicly pullable.
	IsPublic bool `json:"is_public"`
	// StorageQuota is the max storage in bytes; 0 uses the metadata default (10 GB).
	StorageQuota int64 `json:"storage_quota"`
}

func (h *Handler) handleCreateRepository(w http.ResponseWriter, r *http.Request) {
	// Cap body size to prevent large-payload attacks before decoding.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var body createRepositoryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate names against CLAUDE.md §7 allowlists before any gRPC call.
	if err := validateOrgName(body.Org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())

	// Pass the full "org/name" form as the name field. The metadata service
	// parses this, upserts the org, and returns the populated org_id — so we
	// leave org_id empty here and let metadata resolve it.
	repo, err := h.meta.CreateRepository(r.Context(), &metadatav1.CreateRepositoryRequest{
		TenantId:     tenantID,
		Name:         body.Org + "/" + body.Name,
		IsPublic:     body.IsPublic,
		StorageQuota: body.StorageQuota,
	})
	if err != nil {
		slog.Error("CreateRepository", "err", err, "org", body.Org, "name", body.Name)
		writeError(w, http.StatusInternalServerError, "failed to create repository")
		return
	}

	writeJSON(w, http.StatusCreated, repoToResponse(repo))
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}
// ---------------------------------------------------------------------------

func (h *Handler) handleGetRepository(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, repoToResponse(repo))
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/repositories/{org}/{repo}
// ---------------------------------------------------------------------------

func (h *Handler) handleDeleteRepository(w http.ResponseWriter, r *http.Request) {
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

	// Resolve the repo_id — DeleteRepositoryRequest requires an ID, not a name.
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	if _, err := h.meta.DeleteRepository(r.Context(), &metadatav1.DeleteRepositoryRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
	}); err != nil {
		slog.Error("DeleteRepository", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to delete repository")
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
// DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}
// ---------------------------------------------------------------------------

func (h *Handler) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
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

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// DeleteTagRequest accepts a tag name directly — no need to look up a tag_id.
	if _, err := h.meta.DeleteTag(r.Context(), &metadatav1.DeleteTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	}); err != nil {
		slog.Error("DeleteTag", "err", err, "repo_id", repo.GetRepoId(), "tag", tagName)
		writeError(w, http.StatusInternalServerError, "failed to delete tag")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan
// ---------------------------------------------------------------------------

// ScanResponse is the JSON representation of the latest scan result for a tag.
type ScanResponse struct {
	ScanID         string           `json:"scan_id"`
	Status         string           `json:"status"`          // pending|running|complete|failed
	ScannerName    string           `json:"scanner_name"`
	ScannerVersion string           `json:"scanner_version"`
	SeverityCounts map[string]int32 `json:"severity_counts"` // CRITICAL|HIGH|MEDIUM|LOW|NEGLIGIBLE → count
	FindingsJSON   []byte           `json:"findings_json,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	CompletedAt    time.Time        `json:"completed_at"`
}

func (h *Handler) handleGetScan(w http.ResponseWriter, r *http.Request) {
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

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Resolve the manifest digest for this tag — GetScanResult is keyed by digest.
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		slog.Error("GetTag for scan lookup", "err", err, "tag", tagName)
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	result, err := h.meta.GetScanResult(r.Context(), &metadatav1.GetScanResultRequest{
		ManifestDigest: tag.GetManifestDigest(),
		RepoId:         repo.GetRepoId(),
		TenantId:       tenantID,
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
// POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan
// ---------------------------------------------------------------------------

// scanTriggerResponse is returned when a scan request is accepted.
type scanTriggerResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// handleTriggerScan validates the request, then publishes a scan.queued event to
// RabbitMQ so registry-scanner picks it up outside the normal push.completed flow.
func (h *Handler) handleTriggerScan(w http.ResponseWriter, r *http.Request) {
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

	// Verify the repo and tag exist before accepting the request — return 404
	// rather than a misleading 202 if the caller has a typo in the name.
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	// Build the scan.queued event envelope and publish it. The scanner service
	// binds a second consumer on scan.queued (in addition to push.completed) so
	// manually triggered scans reach the same worker pool.
	payload, _ := json.Marshal(events.ScanQueuedPayload{
		TenantID:       tenantID,
		RepositoryName: org + "/" + repoName,
		RepoID:         repo.GetRepoId(),
		TagName:        tagName,
		ManifestDigest: tag.GetManifestDigest(),
	})
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingScanQueued,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(r.Context(), events.RoutingScanQueued, evt); err != nil {
		slog.Error("publish scan.queued", "err", err, "repo", org+"/"+repoName, "tag", tagName)
		writeError(w, http.StatusInternalServerError, "failed to queue scan")
		return
	}

	writeJSON(w, http.StatusAccepted, scanTriggerResponse{
		Status:  "queued",
		Message: "scan request accepted",
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds
// ---------------------------------------------------------------------------

// BuildResponse is the JSON representation of a single build run.
// Fields align with the BuildRow type used in the frontend builds.tsx screen.
type BuildResponse struct {
	BuildID     string `json:"build_id"`
	Status      string `json:"status"`       // "in_progress" | "success" | "failed"
	CommitHash  string `json:"commit_hash"`
	TriggeredBy string `json:"triggered_by"` // actor login or CI system name
	Duration    string `json:"duration"`     // formatted, e.g. "3m 45s" or "--" when running
	Timestamp   string `json:"timestamp"`    // relative age, e.g. "2m ago"
}

// handleListBuilds returns the push/build history for a specific tag by
// querying the audit service's GetBuildHistory RPC.
func (h *Handler) handleListBuilds(w http.ResponseWriter, r *http.Request) {
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

	// Return 404 for unknown repos rather than an empty build list that could
	// be confused with "no builds yet" on a non-existent repo.
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Optional limit from query string; default is 25 (the audit service applies its own cap).
	limit := int32(25)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, parseErr := strconv.Atoi(s); parseErr == nil && n > 0 && n <= 100 {
			limit = int32(n)
		}
	}

	resp, err := h.audit.GetBuildHistory(r.Context(), &auditv1.GetBuildHistoryRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		Tag:      tagName,
		Limit:    limit,
	})
	if err != nil {
		slog.Error("GetBuildHistory", "err", err, "repo_id", repo.GetRepoId(), "tag", tagName)
		writeError(w, http.StatusInternalServerError, "failed to fetch build history")
		return
	}

	builds := make([]BuildResponse, 0, len(resp.GetBuilds()))
	for _, b := range resp.GetBuilds() {
		// Format the occurred_at timestamp as a relative-age string for the frontend.
		ts := b.GetOccurredAt().AsTime()
		builds = append(builds, BuildResponse{
			BuildID:     b.GetBuildId(),
			Status:      b.GetStatus(),
			CommitHash:  b.GetCommitHash(),
			TriggeredBy: b.GetTriggeredBy(),
			Duration:    b.GetDuration(),
			Timestamp:   formatRelativeAge(ts),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"builds": builds,
		"total":  resp.GetTotal(),
	})
}

// formatRelativeAge converts t to a human-readable relative string such as
// "2m ago" or "3h ago". Used for the build history Timestamp field.
func formatRelativeAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// findRepo resolves a repository by its org + short name within a tenant.
//
// The metadata service stores the full name as "org/repo". This helper
// constructs the full name and scans the ListRepositories stream for a match.
//
// This is O(n) over all tenant repositories — a known limitation bounded by
// authentication. PageSize is capped at 100 per page to limit gRPC blast radius;
// up to 10 pages are fetched (1,000 repos max before we give up).
// TODO: replace with a GetRepositoryByName gRPC RPC once added to the
// MetadataService proto (requires buf generate; proto stubs cannot be
// hand-edited — see CLAUDE.md §15 for proto conventions).
func (h *Handler) findRepo(r *http.Request, tenantID, org, repoName string) (*metadatav1.Repository, error) {
	fullName := org + "/" + repoName

	stream, err := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
		// 100 per page — smaller than the previous 1000 to bound amplification
		// while still covering most tenants in a single round-trip.
		PageSize: 100,
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
		// Compare against the full "org/repo" name as stored in metadata.
		if repo.GetName() == fullName {
			return repo, nil
		}
	}

	// Do not include tenantID in the error string — callers that log the error
	// value would expose it; the tenant ID is already available in the request
	// context and will be captured by the structured logging interceptor.
	return nil, fmt.Errorf("repository not found")
}

// repoToResponse converts a proto Repository message to its JSON wire form.
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

// writeJSON sets Content-Type, writes the given status code, and encodes v as JSON.
// Errors from the encoder are logged but not returned to the caller — the
// response headers are already sent at that point.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode", "err", err)
	}
}

// writeError writes a generic {"error": msg} JSON body with the given status.
// The message must never contain internal details (gRPC status codes, service
// names, stack traces) — see CLAUDE.md §4.13 error response rules.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
