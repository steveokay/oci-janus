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
	"context"
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
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// maxBodyBytes caps incoming JSON request bodies to prevent large-payload attacks.
const maxBodyBytes = 4096

// Handler holds gRPC client dependencies for all management endpoints.
type Handler struct {
	auth  authv1.AuthServiceClient
	meta  metadatav1.MetadataServiceClient
	audit auditv1.AuditServiceClient
	// pub publishes events to the registry.events RabbitMQ exchange.
	pub           *publisher.Publisher
	healthClients []healthpb.HealthClient
	// platformAdminTenantID is the tenant whose admin/owner users can call cross-tenant
	// platform operations (e.g. setting another tenant's storage quota). Empty disables
	// the route entirely. Set via PLATFORM_ADMIN_TENANT_ID env var.
	platformAdminTenantID string
	// rateLimiter applies PENTEST-014 per-user rate limiting after RequireAuth.
	// Optional — when nil, every authenticated request passes through unthrottled
	// (useful for tests that want deterministic timing).
	rateLimiter *middleware.PerUserRateLimiter
}

// New creates a Handler wired to the given gRPC clients and RabbitMQ publisher.
// healthClients are optional; when provided they are polled by handleStats to compute SystemHealthPct.
func New(
	auth authv1.AuthServiceClient,
	meta metadatav1.MetadataServiceClient,
	audit auditv1.AuditServiceClient,
	pub *publisher.Publisher,
	platformAdminTenantID string,
	healthClients ...healthpb.HealthClient,
) *Handler {
	return &Handler{
		auth:                  auth,
		meta:                  meta,
		audit:                 audit,
		pub:                   pub,
		platformAdminTenantID: platformAdminTenantID,
		healthClients:         healthClients,
	}
}

// WithRateLimiter attaches a per-user rate limiter that runs after RequireAuth
// for every authenticated route (PENTEST-014). Returns the handler for chained
// initialization. Call before Register.
func (h *Handler) WithRateLimiter(l *middleware.PerUserRateLimiter) *Handler {
	h.rateLimiter = l
	return h
}

// checkServicesHealth calls the gRPC health check on each configured service and
// returns the percentage (0–100) that are currently SERVING.
// Uses a 2-second deadline so a slow or unreachable service never stalls the stats page.
func (h *Handler) checkServicesHealth(ctx context.Context) float64 {
	if len(h.healthClients) == 0 {
		return 100.0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	healthy := 0
	for _, c := range h.healthClients {
		resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{Service: ""})
		if err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			healthy++
		}
	}
	return float64(healthy) / float64(len(h.healthClients)) * 100.0
}

// Register mounts all management routes onto mux.
//
// /healthz is open (no auth). Every other route is wrapped with RequireAuth,
// which validates the Bearer token via registry-auth gRPC and stores the
// tenant_id in the request context. The PerUserRateLimiter (PENTEST-014)
// runs immediately after RequireAuth, so user_id is available for keying.
//
// Route patterns use Go 1.22+ net/http syntax: {param} captures a single
// path segment, {param...} captures the remainder.
func (h *Handler) Register(mux *http.ServeMux) {
	rawAuthMW := middleware.RequireAuth(h.auth)
	// PENTEST-014: per-user rate limit. Default 20 rps with a burst of 40 is
	// generous for an interactive dashboard but blocks a runaway script. With
	// multiple management replicas the cluster-wide cap is N×, which is fine
	// for a defence-in-depth gate; the limiter is in-process by design.
	authMW := rawAuthMW
	if h.rateLimiter != nil {
		authMW = func(next http.Handler) http.Handler {
			return rawAuthMW(h.rateLimiter.Middleware(next))
		}
	}

	// Health — unauthenticated, used by docker-compose and K8s probes.
	mux.Handle("GET /healthz", http.HandlerFunc(handleHealthz))

	// Tenant-scoped aggregate stats.
	mux.Handle("GET /api/v1/stats", authMW(http.HandlerFunc(h.handleStats)))

	// Repository management.
	// POST and DELETE require admin role or above (enforced in handler body).
	mux.Handle("GET /api/v1/repositories", authMW(http.HandlerFunc(h.handleListRepositories)))
	mux.Handle("POST /api/v1/repositories", authMW(http.HandlerFunc(h.handleCreateRepository)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleGetRepository)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleDeleteRepository)))

	// Tag management.
	// DELETE requires writer role or above (enforced in handler body).
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags", authMW(http.HandlerFunc(h.handleListTags)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}", authMW(http.HandlerFunc(h.handleDeleteTag)))

	// Vulnerability scanning — tag-scoped per CLAUDE.md §4.13.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleGetScan)))
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleTriggerScan)))

	// Build / audit history — returns empty list until registry-audit query API is ready.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds", authMW(http.HandlerFunc(h.handleListBuilds)))

	// RBAC management — org and repo membership endpoints.
	h.RegisterRBAC(mux, authMW)

	// Platform-admin: set tenant-level storage quota. Caller must be admin/owner
	// AND must belong to the configured platform-admin tenant. This route is the
	// canonical way to bump quotas for large customers.
	mux.Handle("PUT /api/v1/admin/tenants/{tenantID}/quota", authMW(http.HandlerFunc(h.handleSetTenantQuota)))
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
	DailyPulls         int64   `json:"daily_pulls"`
	VulnerabilityCount int     `json:"vulnerability_count"`
	SystemHealthPct    float64 `json:"system_health_pct"`
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

	countResp, err := h.meta.CountRepositories(r.Context(), &metadatav1.CountRepositoriesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("CountRepositories", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to count repositories")
		return
	}

	vulns, err := h.meta.GetTenantVulnerabilityCount(r.Context(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("GetTenantVulnerabilityCount", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch vulnerability counts")
		return
	}

	var dailyPulls int64
	if pullResp, pullErr := h.audit.GetDailyPullCount(r.Context(), &auditv1.GetDailyPullCountRequest{
		TenantId: tenantID,
	}); pullErr == nil {
		dailyPulls = pullResp.GetCount()
	} else {
		slog.Warn("GetDailyPullCount", "err", pullErr)
	}

	writeJSON(w, http.StatusOK, StatsResponse{
		TotalRepos:         int(countResp.GetCount()),
		StorageUsedBytes:   quota.GetUsedBytes(),
		StorageQuotaBytes:  quota.GetQuotaBytes(),
		DailyPulls:         dailyPulls,
		VulnerabilityCount: int(vulns.GetTotal()),
		SystemHealthPct:    h.checkServicesHealth(r.Context()),
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

	// PENTEST-002: admin/owner OF THIS ORG (not anywhere in the tenant) may create.
	// Authz happens after name validation so we don't leak whether an org exists
	// via a 403-vs-400 distinction.
	if !hasScopedRole(h.getUserAssignments(r), "org", body.Org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
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

	// PENTEST-002: admin/owner on THIS REPO (or its parent org) may delete.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
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

	// PENTEST-002: writer or above on THIS REPO (or parent org) may delete tags.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
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

	// PENTEST-002: writer or above on THIS REPO may trigger scans. Scans cost
	// real CPU + bandwidth on the scanner pool, so let only push-capable users
	// queue them, not readers.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
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

// findRepo resolves a repository by its org + short name within a tenant via a
// direct GetRepositoryByName gRPC call. This replaces the previous O(n) stream
// scan over ListRepositories and executes as a single indexed SQL lookup in
// registry-metadata (see GetRepositoryByFullName in the metadata repository layer).
func (h *Handler) findRepo(r *http.Request, tenantID, org, repoName string) (*metadatav1.Repository, error) {
	repo, err := h.meta.GetRepositoryByName(r.Context(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: tenantID,
		Name:     org + "/" + repoName,
	})
	if err != nil {
		// Do not include tenantID in the error string — callers that log the error
		// value would expose it; the tenant ID is captured by the structured
		// logging interceptor on the request context.
		return nil, fmt.Errorf("repository not found")
	}
	return repo, nil
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

// ---------------------------------------------------------------------------
// PUT /api/v1/admin/tenants/{tenantID}/quota
// ---------------------------------------------------------------------------

// setTenantQuotaRequest is the JSON body for the quota route.
type setTenantQuotaRequest struct {
	QuotaBytes int64 `json:"quota_bytes"`
}

// setTenantQuotaResponse mirrors the metadata QuotaUsage so the caller sees the
// fresh quota + current used bytes after the update.
type setTenantQuotaResponse struct {
	TenantID   string `json:"tenant_id"`
	UsedBytes  int64  `json:"used_bytes"`
	QuotaBytes int64  `json:"quota_bytes"`
}

// handleSetTenantQuota bumps (or lowers) the tenant-level storage quota.
//
// Authorization model (defense in depth):
//   1. PLATFORM_ADMIN_TENANT_ID must be configured (route disabled otherwise).
//   2. The caller's JWT tenant must equal PLATFORM_ADMIN_TENANT_ID — preventing
//      tenants from setting their own quotas (which would defeat the purpose).
//   3. The caller must hold admin or owner role.
//
// The target tenant comes from the URL, not the JWT, so a platform operator can
// adjust any tenant's quota. The endpoint never returns gRPC error detail to the
// caller — only a generic message — to avoid leaking internal service state.
func (h *Handler) handleSetTenantQuota(w http.ResponseWriter, r *http.Request) {
	if h.platformAdminTenantID == "" {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	callerTenant := middleware.TenantIDFromContext(r.Context())
	if callerTenant != h.platformAdminTenantID {
		writeError(w, http.StatusForbidden, "platform admin tenant required")
		return
	}

	// PENTEST-024: require the platform-admin marker grant — not just any admin
	// role in the platform-admin tenant. The marker is a role_assignment with
	// scope_type="org" and the literal scope_value="*"; org names can't contain
	// "*" (validateOrgName rejects it), so this string is unambiguous and never
	// collides with a real org grant. Operators must explicitly grant
	// ("admin", "org", "*") to platform admins.
	if !hasScopedRole(h.getUserAssignments(r), "org", "*", "admin") {
		writeError(w, http.StatusForbidden, "platform-admin role required (org=*, admin)")
		return
	}

	targetTenant := r.PathValue("tenantID")
	if _, err := uuid.Parse(targetTenant); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	var body setTenantQuotaRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.QuotaBytes < 0 {
		writeError(w, http.StatusBadRequest, "quota_bytes must be non-negative")
		return
	}

	usage, err := h.meta.UpdateTenantQuota(r.Context(), &metadatav1.UpdateTenantQuotaRequest{
		TenantId:   targetTenant,
		QuotaBytes: body.QuotaBytes,
	})
	if err != nil {
		slog.Error("UpdateTenantQuota", "err", err, "target_tenant", targetTenant)
		writeError(w, http.StatusInternalServerError, "failed to update quota")
		return
	}

	writeJSON(w, http.StatusOK, setTenantQuotaResponse{
		TenantID:   usage.GetTenantId(),
		UsedBytes:  usage.GetUsedBytes(),
		QuotaBytes: usage.GetQuotaBytes(),
	})
}
