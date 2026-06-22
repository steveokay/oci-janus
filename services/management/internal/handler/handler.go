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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// maxBodyBytes caps incoming JSON request bodies to prevent large-payload attacks.
const maxBodyBytes = 4096

// EventPublisher is the narrow contract the management handler needs from the
// RabbitMQ publisher. Exported so external tests in the handler_test package
// can supply a fake without dragging in amqp091. *publisher.Publisher
// satisfies this interface.
type EventPublisher interface {
	Publish(ctx context.Context, routingKey string, event events.Event) error
}

// Handler holds gRPC client dependencies for all management endpoints.
type Handler struct {
	auth  authv1.AuthServiceClient
	meta  metadatav1.MetadataServiceClient
	audit auditv1.AuditServiceClient
	// tenant is optional — wired only when TENANT_GRPC_ADDR is set. nil disables
	// the super-admin `/api/v1/admin/tenants` routes (they return 404).
	tenant tenantv1.TenantServiceClient
	// webhook is optional — wired only when WEBHOOK_GRPC_ADDR is set. nil
	// disables the `/api/v1/webhooks` family (they return 404 "route disabled").
	webhook webhookv1.WebhookServiceClient
	// signer is optional — wired only when SIGNER_GRPC_ADDR is set. nil
	// disables the `/api/v1/.../signature` route (FE-API-003); it returns
	// 404 "route disabled" so the frontend can render the unsigned state
	// instead of an error.
	signer signerv1.SignerServiceClient
	// scanner is optional — wired only when SCANNER_GRPC_ADDR is set.
	// nil disables the FE-API-018 `/api/v1/security/policies` and
	// FE-API-019 `/api/v1/security/reports/*` routes (404 "route disabled").
	scanner scannerv1.ScannerServiceClient
	// gc is optional — wired only when GC_GRPC_ADDR is set. nil
	// disables the FE-API-032 `/api/v1/admin/gc/*` routes (404 "route
	// disabled") so deployments running registry-gc in cron-only mode
	// continue to serve every other surface.
	gc gcv1.GCServiceClient
	// pub publishes events to the registry.events RabbitMQ exchange.
	// Typed as an interface so tests can substitute a fake without standing up
	// a real RabbitMQ broker. *publisher.Publisher satisfies the interface.
	pub           EventPublisher
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
//
// pub is typed as a narrow eventPublisher interface so tests may substitute a
// fake; the production wiring in services/management/internal/server still
// passes a *publisher.Publisher, which satisfies the interface.
func New(
	auth authv1.AuthServiceClient,
	meta metadatav1.MetadataServiceClient,
	audit auditv1.AuditServiceClient,
	pub *publisher.Publisher,
	platformAdminTenantID string,
	healthClients ...healthpb.HealthClient,
) *Handler {
	h := &Handler{
		auth:                  auth,
		meta:                  meta,
		audit:                 audit,
		platformAdminTenantID: platformAdminTenantID,
		healthClients:         healthClients,
	}
	// Guard against the typed-nil-into-interface trap: when callers pass an
	// untyped nil (tests do this for the scan-trigger paths that don't exercise
	// the publisher), we want h.pub == nil to be true so downstream gating works.
	if pub != nil {
		h.pub = pub
	}
	return h
}

// WithPublisher swaps the event publisher. Used by tests to inject a fake
// without touching the production New signature. Returns the handler for
// chained initialization.
func (h *Handler) WithPublisher(p EventPublisher) *Handler {
	h.pub = p
	return h
}

// WithRateLimiter attaches a per-user rate limiter that runs after RequireAuth
// for every authenticated route (PENTEST-014). Returns the handler for chained
// initialization. Call before Register.
func (h *Handler) WithRateLimiter(l *middleware.PerUserRateLimiter) *Handler {
	h.rateLimiter = l
	return h
}

// WithTenantClient enables the super-admin `/api/v1/admin/tenants` routes
// (create/list/delete tenants). When the client is nil, those routes return
// 404 "route disabled" — the same opt-in pattern used by `handleSetTenantQuota`
// when PLATFORM_ADMIN_TENANT_ID is unset.
func (h *Handler) WithTenantClient(c tenantv1.TenantServiceClient) *Handler {
	h.tenant = c
	return h
}

// WithSignerClient enables the `/api/v1/.../signature` route (FE-API-003).
// Nil leaves the route returning 404 "route disabled" so management can
// deploy without registry-signer in environments that don't run image
// signing.
func (h *Handler) WithSignerClient(c signerv1.SignerServiceClient) *Handler {
	h.signer = c
	return h
}

// WithWebhookClient enables the `/api/v1/webhooks` family (CRUD + deliveries
// + test + rotate-secret). Nil disables the routes (404 "route disabled") so
// management can deploy without registry-webhook in environments that don't
// need outbound webhooks.
func (h *Handler) WithWebhookClient(c webhookv1.WebhookServiceClient) *Handler {
	h.webhook = c
	return h
}

// WithGCClient enables the FE-API-032 GC status routes:
//
//	GET  /api/v1/admin/gc/status
//	GET  /api/v1/admin/gc/runs
//	POST /api/v1/admin/gc/run
//
// Nil leaves the routes returning 404 "route disabled". All three are
// also gated by the platform-admin marker grant (org=*, admin) so a
// regular tenant admin cannot inspect or trigger GC sweeps.
func (h *Handler) WithGCClient(c gcv1.GCServiceClient) *Handler {
	h.gc = c
	return h
}

// WithScannerClient enables the FE-API-018 + FE-API-019 routes:
//
//	GET /api/v1/security/policies
//	PUT /api/v1/security/policies
//	POST /api/v1/security/reports/generate
//	GET /api/v1/security/reports
//	GET /api/v1/security/reports/{id}
//	GET /api/v1/security/reports/{id}/download/{pdf|sbom}
//
// Nil leaves all of the above returning 404 "route disabled" so the
// dashboard can render the "scanner unavailable" state without a hard
// error.
func (h *Handler) WithScannerClient(c scannerv1.ScannerServiceClient) *Handler {
	h.scanner = c
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

	// Current workspace / tenant info (FE-API-009).
	mux.Handle("GET /api/v1/workspace/me", authMW(http.HandlerFunc(h.handleGetWorkspace)))

	// Repository management.
	// POST and DELETE require admin role or above (enforced in handler body).
	mux.Handle("GET /api/v1/repositories", authMW(http.HandlerFunc(h.handleListRepositories)))
	mux.Handle("POST /api/v1/repositories", authMW(http.HandlerFunc(h.handleCreateRepository)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleGetRepository)))
	mux.Handle("PATCH /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleUpdateRepository)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}", authMW(http.HandlerFunc(h.handleDeleteRepository)))

	// Tag management.
	// DELETE requires writer role or above (enforced in handler body).
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags", authMW(http.HandlerFunc(h.handleListTags)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}", authMW(http.HandlerFunc(h.handleDeleteTag)))

	// Bulk tag delete (FE-API-036). DELETE with body of tag names; per-tag
	// result returned so partial successes are visible to the UI.
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/tags", authMW(http.HandlerFunc(h.handleBulkDeleteTags)))

	// Per-repo storage breakdown (FE-API-031). Top-50 repos by storage_used
	// plus the tenant total, in one call.
	mux.Handle("GET /api/v1/stats/storage", authMW(http.HandlerFunc(h.handleGetStorageBreakdown)))

	// Manifest detail for a specific tag (FE-API-002).
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/manifest", authMW(http.HandlerFunc(h.handleGetManifest)))

	// Signing verification for a specific tag (FE-API-003). 404 when
	// SIGNER_GRPC_ADDR is unset on the BFF. FE-API-025 layers a
	// ?verify=true query param on top for opt-in cryptographic verification.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/signature", authMW(http.HandlerFunc(h.handleGetSignature)))

	// Sign the current manifest of a tag (FE-API-026). 404 when
	// SIGNER_GRPC_ADDR is unset; 403 unless the caller is repo admin.
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/tags/{tag}/sign", authMW(http.HandlerFunc(h.handleSignManifest)))

	// Vulnerability scanning — tag-scoped per CLAUDE.md §4.13.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleGetScan)))
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan", authMW(http.HandlerFunc(h.handleTriggerScan)))

	// Per-tag SBOM download (FE-API-033). Reader access on the repo is
	// sufficient — the SBOM is equivalent to what a reader could derive by
	// pulling the image themselves. ?format=spdx-json (default) is the only
	// implemented format; cyclonedx-json returns 400.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/sbom", authMW(http.HandlerFunc(h.handleGetTagSBOM)))

	// Build / audit history — returns empty list until registry-audit query API is ready.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds", authMW(http.HandlerFunc(h.handleListBuilds)))

	// FE-API-004 repo-scoped activity feed — wide slice of the audit log for
	// one repo (push, delete, scan, sign). Handler lives in repo_activity.go.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/activity", authMW(http.HandlerFunc(h.handleListRepoActivity)))

	// FE-API-008 tenant-wide notifications feed for the topbar bell. Handler
	// lives in notifications.go. Polled by the dashboard; no SSE/WebSocket.
	mux.Handle("GET /api/v1/notifications", authMW(http.HandlerFunc(h.handleListNotifications)))

	// FE-API-030 pull/push analytics time-series. Per-repo + tenant-wide
	// routes share bucket sizing + metric mapping in analytics_repo.go.
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/analytics", authMW(http.HandlerFunc(h.handleGetRepoAnalytics)))
	mux.Handle("GET /api/v1/stats/analytics", authMW(http.HandlerFunc(h.handleGetTenantAnalytics)))

	// RBAC management — org and repo membership endpoints.
	h.RegisterRBAC(mux, authMW)

	// Security overview (FE-API-020) — single tenant-scoped aggregate.
	h.RegisterSecurity(mux, authMW)

	// Scan policies (FE-API-018) + compliance reports (FE-API-019). Routes
	// return 404 when SCANNER_GRPC_ADDR is unset.
	h.RegisterSecurityPolicies(mux, authMW)
	h.RegisterSecurityReports(mux, authMW)

	// Webhook management — CRUD + deliveries + test + rotate-secret.
	// Routes return 404 when h.webhook is nil (WEBHOOK_GRPC_ADDR unset).
	h.RegisterWebhooks(mux, authMW)

	// Workspace custom-domain CRUD (FE-API-027). Routes return 404 when
	// h.tenant is nil (TENANT_GRPC_ADDR unset).
	h.RegisterWorkspaceDomains(mux, authMW)

	// Per-repo retention policy CRUD (FE-API-037). All routes require at
	// least reader on the repo (GET) or repo admin (PUT/DELETE). The
	// executor + dry-run + events arrive in FE-API-040/038/041.
	h.RegisterRepoRetention(mux, authMW)

	// FE-API-038: dry-run + preview-window state. POST dry-run requires
	// repo admin (same gate as PUT); GET preview requires repo reader.
	// Both delegate to the metadata EvaluateRetention RPC — read-only,
	// never persists.
	h.RegisterRepoRetentionDryRun(mux, authMW)

	// FE-API-040: retention executor trigger + per-run status. POST .../run
	// requires repo admin / owner (writer not enough — retention deletes
	// manifests). GET .../runs/{run_id} requires reader. Both return 404
	// when GC_GRPC_ADDR is unset.
	h.RegisterRepoRetentionRun(mux, authMW)

	// FE-API-039: per-org default retention policy. GET requires org reader;
	// PUT/DELETE require org admin (writer not enough — retention is
	// destructive). The per-repo GET above also gains an inheritance
	// fallback so callers can read the org default through the repo URL
	// when no per-repo policy exists.
	h.RegisterOrgRetention(mux, authMW)

	// FE-API-049: org-default + per-repo scan policies. Mirrors the
	// retention CRUD posture above — reader on read paths, admin/owner
	// on writes. Both surfaces depend on h.scanner; the registrations
	// themselves succeed even when SCANNER_GRPC_ADDR is unset (the
	// handlers return 404 "route disabled" individually). Effective
	// policy resolution lives in the scanner via GetEffectiveScanPolicy.
	h.RegisterOrgScanPolicy(mux, authMW)
	h.RegisterRepoScanPolicy(mux, authMW)

	// FE-API-050: manifest quarantine surface. Just one route today
	// (POST .../quarantine/lift) — the scanner sets quarantine
	// automatically based on the effective scan policy, this route
	// lets a repo admin/owner dismiss it after operator review.
	h.RegisterManifestQuarantine(mux, authMW)

	// Platform-admin: set tenant-level storage quota. Caller must be admin/owner
	// AND must belong to the configured platform-admin tenant. This route is the
	// canonical way to bump quotas for large customers.
	mux.Handle("PUT /api/v1/admin/tenants/{tenantID}/quota", authMW(http.HandlerFunc(h.handleSetTenantQuota)))

	// Platform-admin: tenant CRUD. Gated by the platform-admin marker scope
	// (admin / org / *) — see services/management/internal/handler/admin_tenants.go.
	// Routes return 404 "route disabled" when TENANT_GRPC_ADDR is unset.
	mux.Handle("GET /api/v1/admin/tenants", authMW(http.HandlerFunc(h.handleAdminListTenants)))
	mux.Handle("POST /api/v1/admin/tenants", authMW(http.HandlerFunc(h.handleAdminCreateTenant)))
	mux.Handle("GET /api/v1/admin/tenants/{tenantID}", authMW(http.HandlerFunc(h.handleAdminGetTenant)))
	mux.Handle("DELETE /api/v1/admin/tenants/{tenantID}", authMW(http.HandlerFunc(h.handleAdminDeleteTenant)))
	// FE-API-029: rename + plan change. Patch body accepts optional name/plan
	// fields; emits tenant.renamed / tenant.plan_changed RabbitMQ events.
	mux.Handle("PATCH /api/v1/admin/tenants/{tenantID}", authMW(http.HandlerFunc(h.handleAdminUpdateTenant)))

	// FE-API-032 — GC status visibility. Status + runs history are
	// read-only; the trigger requires the platform-admin marker. All
	// three routes return 404 "route disabled" when GC_GRPC_ADDR is unset.
	mux.Handle("GET /api/v1/admin/gc/status", authMW(http.HandlerFunc(h.handleAdminGCStatus)))
	mux.Handle("GET /api/v1/admin/gc/runs", authMW(http.HandlerFunc(h.handleAdminGCRuns)))
	mux.Handle("POST /api/v1/admin/gc/run", authMW(http.HandlerFunc(h.handleAdminGCRun)))

	// FE-API-044..047 — platform-admin scanner adapter management
	// (REM-011 Phase 2). All five routes return 404 "route disabled"
	// when SCANNER_GRPC_ADDR is unset and 403 when the caller lacks
	// the platform-admin marker grant. See admin_scanners.go.
	h.RegisterAdminScanners(mux, authMW)
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

// SeverityCounts is the per-severity vulnerability breakdown shared by
// /api/v1/stats (FE-API-016) and /api/v1/security/overview (FE-API-020).
// All counts are int32 to match the underlying scanner plugin payloads.
type SeverityCounts struct {
	Critical   int32 `json:"critical"`
	High       int32 `json:"high"`
	Medium     int32 `json:"medium"`
	Low        int32 `json:"low"`
	Negligible int32 `json:"negligible"`
}

// StatsResponse is the JSON body returned by GET /api/v1/stats.
type StatsResponse struct {
	TotalRepos         int     `json:"total_repos"`
	StorageUsedBytes   int64   `json:"storage_used_bytes"`
	StorageQuotaBytes  int64   `json:"storage_quota_bytes"`
	DailyPulls         int64   `json:"daily_pulls"`
	VulnerabilityCount int     `json:"vulnerability_count"`
	SystemHealthPct    float64 `json:"system_health_pct"`
	// Per-severity breakdown (FE-API-016). Both root-level *_count fields and
	// the nested severity_counts object are emitted so existing consumers do
	// not break while the dashboard migrates to the nested shape.
	CriticalCount   int64          `json:"critical_count"`
	HighCount       int64          `json:"high_count"`
	MediumCount     int64          `json:"medium_count"`
	LowCount        int64          `json:"low_count"`
	NegligibleCount int64          `json:"negligible_count"`
	SeverityCounts  SeverityCounts `json:"severity_counts"`
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
		CriticalCount:      vulns.GetCriticalCount(),
		HighCount:          vulns.GetHighCount(),
		MediumCount:        vulns.GetMediumCount(),
		LowCount:           vulns.GetLowCount(),
		NegligibleCount:    vulns.GetNegligibleCount(),
		// FE-API-016: nested object the frontend severity bar reads. The
		// proto VulnerabilityCountResponse uses int64 internally but the
		// scanner payloads are int32 — narrow without overflow concerns
		// (no tenant has 2.1B findings).
		SeverityCounts: SeverityCounts{
			Critical:   int32(vulns.GetCriticalCount()),
			High:       int32(vulns.GetHighCount()),
			Medium:     int32(vulns.GetMediumCount()),
			Low:        int32(vulns.GetLowCount()),
			Negligible: int32(vulns.GetNegligibleCount()),
		},
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories
// ---------------------------------------------------------------------------

// RepoResponse is the JSON representation of a single repository.
type RepoResponse struct {
	RepoID string `json:"repo_id"`
	OrgID  string `json:"org_id"`
	// Org is the parent organisation's human name (e.g. "dev"), JOINed from
	// `organizations.name` so callers can render `/repositories/{org}/{name}`
	// without a second lookup (FE-API-010).
	Org          string    `json:"org"`
	Name         string    `json:"name"`
	IsPublic     bool      `json:"is_public"`
	StorageUsed  int64     `json:"storage_used_bytes"`
	StorageQuota int64     `json:"storage_quota_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	Description  string    `json:"description"`
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
	// Description is an optional markdown README for the repository (FE-API-006).
	Description string `json:"description"`
}

// updateRepositoryBody is the expected JSON body for PATCH /api/v1/repositories/{org}/{repo}.
type updateRepositoryBody struct {
	Description string `json:"description"`
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
		Description:  body.Description,
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
	Name           string `json:"name"`
	ManifestDigest string `json:"manifest_digest"`
	// SizeBytes is the total image size — config blob + sum of layer blob
	// sizes for an image manifest, or sum of child manifest sizes for an
	// image index. Computed at push time and stored on `manifests`; 0 for
	// pre-FE-API-001 rows that haven't been re-pushed since the backfill.
	SizeBytes int64     `json:"size_bytes"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
	// REM-013 gap 1: surfaces manifests.retention_pending_delete_at via
	// proto Tag.retention_pending_delete_at so the dashboard can render
	// "🗑 deletes in N days" pills on the Tags table. Omitted when the
	// referenced manifest has no pending delete stamp (the common case).
	RetentionPendingDeleteAt *time.Time `json:"retention_pending_delete_at,omitempty"`
	// FE-API-050: parent manifest's quarantined flag. The dashboard
	// renders a 🔒 pill on quarantined rows; clicking the badge opens
	// the quarantine detail / lift dialog on the tag detail page.
	Quarantined bool `json:"quarantined,omitempty"`
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
		out := TagResponse{
			Name:           tag.GetName(),
			ManifestDigest: tag.GetManifestDigest(),
			SizeBytes:      tag.GetSizeBytes(),
			UpdatedAt:      tag.GetUpdatedAt().AsTime(),
			CreatedAt:      tag.GetCreatedAt().AsTime(),
			// FE-API-050: surface the parent manifest's quarantine flag
			// on every tag row so the dashboard renders a 🔒 pill
			// without per-row GetManifest calls.
			Quarantined: tag.GetQuarantined(),
		}
		// REM-013 gap 1: surface the soft-delete stamp only when the
		// upstream proto carries one. The proto's GetX() helper returns
		// nil for unset Timestamps, so we check explicitly rather than
		// emitting a zero time on every row.
		if ts := tag.GetRetentionPendingDeleteAt(); ts != nil {
			t := ts.AsTime()
			out.RetentionPendingDeleteAt = &t
		}
		tags = append(tags, out)
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
		// No scan recorded yet is the normal "operator hasn't triggered a
		// scan" state — surface as 404 so the dashboard renders its
		// "Trigger scan" CTA instead of an error banner. Anything else is
		// a real failure worth a 500.
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "no scan recorded")
			return
		}
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
		Org:          r.GetOrg(),
		Name:         r.GetName(),
		IsPublic:     r.GetIsPublic(),
		StorageUsed:  r.GetStorageUsed(),
		StorageQuota: r.GetStorageQuota(),
		CreatedAt:    r.GetCreatedAt().AsTime(),
		Description:  r.GetDescription(),
	}
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/repositories/{org}/{repo}   (FE-API-006)
// ---------------------------------------------------------------------------

func (h *Handler) handleUpdateRepository(w http.ResponseWriter, r *http.Request) {
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

	// Only admins/owners on this repo (or parent org) may update metadata.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var body updateRepositoryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Resolve repo_id — UpdateRepository RPC is keyed by ID, not name.
	existing, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	repo, err := h.meta.UpdateRepository(r.Context(), &metadatav1.UpdateRepositoryRequest{
		TenantId:    tenantID,
		RepoId:      existing.GetRepoId(),
		Description: body.Description,
	})
	if err != nil {
		slog.Error("UpdateRepository", "err", err, "repo_id", existing.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to update repository")
		return
	}
	writeJSON(w, http.StatusOK, repoToResponse(repo))
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/manifest   (FE-API-002)
// ---------------------------------------------------------------------------

// manifestLayer is a single layer entry extracted from the manifest JSON.
type manifestLayer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// manifestConfig holds the image config descriptor extracted from the manifest JSON.
type manifestConfig struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// ManifestResponse is the JSON body for GET …/tags/{tag}/manifest.
//
// For single-arch image manifests `Config` + `Layers` carry the detail.
// For OCI image indexes / Docker manifest lists `Manifests` is populated
// and `Config` + `Layers` are empty. `IsIndex` is the one explicit signal
// the client can branch on without sniffing media types.
type ManifestResponse struct {
	Digest    string          `json:"digest"`
	MediaType string          `json:"media_type"`
	SizeBytes int64           `json:"size_bytes"`
	CreatedAt time.Time       `json:"created_at"`
	IsIndex   bool            `json:"is_index"`
	Config    manifestConfig  `json:"config"`
	Layers    []manifestLayer `json:"layers"`
	Manifests []manifestEntry `json:"manifests"`
	// FE-API-050 — quarantine state surfaced so the tag-detail Security
	// tab can render a "Quarantined" banner with the reason + who
	// quarantined + when, and the "Lift quarantine" button. All four
	// fields are omitted on the wire when the manifest is not
	// quarantined (the common case).
	Quarantined       bool       `json:"quarantined,omitempty"`
	QuarantineReason  string     `json:"quarantine_reason,omitempty"`
	QuarantinedAt     *time.Time `json:"quarantined_at,omitempty"`
	QuarantinedBy     string     `json:"quarantined_by,omitempty"`
}

// rawManifest is the subset of an OCI/Docker manifest JSON we need to parse.
// Covers both single-arch image manifests (config + layers) and image
// indexes / Docker manifest lists (manifests[] with per-platform entries).
type rawManifest struct {
	Config struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Layers []struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
	} `json:"layers"`
	// Manifests is populated for OCI image indexes
	// (application/vnd.oci.image.index.v1+json) and Docker manifest lists
	// (application/vnd.docker.distribution.manifest.list.v2+json). Each
	// entry points at a per-platform child manifest.
	Manifests []struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
			OSVersion    string `json:"os.version"`
		} `json:"platform"`
	} `json:"manifests"`
}

// manifestEntry surfaces a single child manifest of an OCI index — what the
// dashboard renders as a per-platform row.
type manifestEntry struct {
	Digest       string `json:"digest"`
	Size         int64  `json:"size"`
	MediaType    string `json:"media_type"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
	OSVersion    string `json:"os_version,omitempty"`
}

func (h *Handler) handleGetManifest(w http.ResponseWriter, r *http.Request) {
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

	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	m, err := h.meta.GetManifest(r.Context(), &metadatav1.GetManifestRequest{
		RepoId:    repo.GetRepoId(),
		TenantId:  tenantID,
		Reference: tag.GetManifestDigest(),
	})
	if err != nil {
		slog.Error("GetManifest", "err", err, "digest", tag.GetManifestDigest())
		writeError(w, http.StatusInternalServerError, "failed to fetch manifest")
		return
	}

	resp := ManifestResponse{
		Digest:    m.GetDigest(),
		MediaType: m.GetMediaType(),
		SizeBytes: m.GetSizeBytes(),
		CreatedAt: m.GetCreatedAt().AsTime(),
		Layers:    []manifestLayer{},
		Manifests: []manifestEntry{},
	}
	// FE-API-050: surface quarantine fields so the tag-detail Security
	// tab can render the banner + lift dialog. Only emit when actually
	// quarantined — omitempty on the wire keeps the common case clean.
	if m.GetQuarantined() {
		resp.Quarantined = true
		resp.QuarantineReason = m.GetQuarantineReason()
		resp.QuarantinedBy = m.GetQuarantinedBy()
		if ts := m.GetQuarantinedAt(); ts != nil {
			t := ts.AsTime()
			resp.QuarantinedAt = &t
		}
	}

	var raw rawManifest
	if err := json.Unmarshal(m.GetRawJson(), &raw); err == nil {
		resp.Config = manifestConfig{
			Digest:    raw.Config.Digest,
			Size:      raw.Config.Size,
			MediaType: raw.Config.MediaType,
		}
		for _, l := range raw.Layers {
			resp.Layers = append(resp.Layers, manifestLayer{
				Digest:    l.Digest,
				Size:      l.Size,
				MediaType: l.MediaType,
			})
		}
		// Index manifests / Docker manifest lists — populate per-platform
		// entries so the UI can render an arch/os table. `IsIndex` is true
		// when the manifest reports any child entries; we don't gate on
		// mediaType alone because Docker and OCI use different strings.
		for _, entry := range raw.Manifests {
			resp.Manifests = append(resp.Manifests, manifestEntry{
				Digest:       entry.Digest,
				Size:         entry.Size,
				MediaType:    entry.MediaType,
				Architecture: entry.Platform.Architecture,
				OS:           entry.Platform.OS,
				Variant:      entry.Platform.Variant,
				OSVersion:    entry.Platform.OSVersion,
			})
		}
		resp.IsIndex = len(resp.Manifests) > 0
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// GET /api/v1/workspace/me   (FE-API-009)
// ---------------------------------------------------------------------------

// WorkspaceDomainEntry mirrors tenantv1.DomainEntry in the REST shape. Carries
// just enough state for the dashboard's domain settings page: the hostname,
// whether the DNS TXT challenge succeeded, and which one is the canonical
// registry hostname (FE-API-007).
type WorkspaceDomainEntry struct {
	Domain    string `json:"domain"`
	Verified  bool   `json:"verified"`
	IsPrimary bool   `json:"is_primary"`
}

// WorkspaceResponse is the JSON body for GET /api/v1/workspace/me.
//
// FE-API-009 expanded shape: Slug + Host + HostIsCustom + Domains. Host is the
// resolved registry hostname for `docker login` / `docker push`; HostIsCustom
// distinguishes a verified custom domain from the wildcard fallback so the
// dashboard can label the source.
type WorkspaceResponse struct {
	TenantID     string                 `json:"tenant_id"`
	Name         string                 `json:"name"`
	Slug         string                 `json:"slug"`
	Plan         string                 `json:"plan"`
	Host         string                 `json:"host"`
	HostIsCustom bool                   `json:"host_is_custom"`
	Domains      []WorkspaceDomainEntry `json:"domains"`
	CreatedAt    time.Time              `json:"created_at"`
}

func (h *Handler) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	t, err := h.tenant.GetTenant(r.Context(), &tenantv1.GetTenantRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("GetTenant", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to fetch workspace")
		return
	}

	// Translate proto domains to the REST shape. Always emit a non-nil slice so
	// the frontend doesn't have to guard against null.
	domains := make([]WorkspaceDomainEntry, 0, len(t.GetDomains()))
	for _, d := range t.GetDomains() {
		domains = append(domains, WorkspaceDomainEntry{
			Domain:    d.GetDomain(),
			Verified:  d.GetVerified(),
			IsPrimary: d.GetIsPrimary(),
		})
	}

	writeJSON(w, http.StatusOK, WorkspaceResponse{
		TenantID:     t.GetTenantId(),
		Name:         t.GetName(),
		Slug:         t.GetSlug(),
		Plan:         t.GetPlan(),
		Host:         t.GetHost(),
		HostIsCustom: t.GetHostIsCustom(),
		Domains:      domains,
		CreatedAt:    t.GetCreatedAt().AsTime(),
	})
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
