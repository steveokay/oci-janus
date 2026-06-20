// Package handler — security overview endpoint (FE-API-020).
//
// This file is intentionally scoped tightly so concurrent edits to handler.go
// (RBAC, webhooks, admin tenants) don't conflict with the security feature
// surface. All shared types (SeverityCounts, writeJSON, writeError) live in
// handler.go.
package handler

import (
	"log/slog"
	"net/http"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ScanCoverageResponse is the nested object that describes how much of the
// tenant's tag inventory has been scanned. percent is `tags_scanned /
// tags_total * 100`, or 0 when there are no tags yet.
type ScanCoverageResponse struct {
	TagsTotal   int64   `json:"tags_total"`
	TagsScanned int64   `json:"tags_scanned"`
	Percent     float64 `json:"percent"`
}

// SecurityOverviewResponse is the JSON body for GET /api/v1/security/overview
// (FE-API-020). All fields are zero-valued on a fresh tenant — callers can
// distinguish "never scanned" from "scanned but clean" via tags_scanned and
// recent_scans_24h.
type SecurityOverviewResponse struct {
	OpenVulnerabilitiesTotal int64                `json:"open_vulnerabilities_total"`
	SeverityCounts           SeverityCounts       `json:"severity_counts"`
	ScanCoverage             ScanCoverageResponse `json:"scan_coverage"`
	RecentScans24h           int64                `json:"recent_scans_24h"`
	DaysSinceLastScan        int64                `json:"days_since_last_scan"`
}

// RegisterSecurity mounts the /api/v1/security/* routes onto mux. The
// surface grew across multiple FE-API tickets (016, 020, 014, 015); each
// handler lives in its own file so concurrent edits don't conflict.
//
// Routes:
//
//	GET /api/v1/security/overview         — FE-API-020 (tenant aggregate)
//	GET /api/v1/security/vulnerabilities  — FE-API-014 (workspace-wide CVE list)
//	GET /api/v1/security/scans            — FE-API-015 (flat scan history)
func (h *Handler) RegisterSecurity(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/security/overview", authMW(http.HandlerFunc(h.handleSecurityOverview)))
	mux.Handle("GET /api/v1/security/vulnerabilities", authMW(http.HandlerFunc(h.handleListVulnerabilities)))
	mux.Handle("GET /api/v1/security/scans", authMW(http.HandlerFunc(h.handleListScanHistory)))
}

// handleSecurityOverview returns the tenant-scoped security summary backing
// the dashboard's security tile. Auth: any authenticated tenant user — no
// extra RBAC scope check beyond tenant membership (FE-API-020 spec). The
// upstream gRPC call already enforces tenant isolation in the SQL CTE.
func (h *Handler) handleSecurityOverview(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	ov, err := h.meta.GetSecurityOverview(r.Context(), &metadatav1.GetSecurityOverviewRequest{
		TenantId: tenantID,
	})
	if err != nil {
		// Don't leak the gRPC status / tenant_id in the user-facing message;
		// the structured log captures both for operators.
		slog.Error("GetSecurityOverview", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to fetch security overview")
		return
	}

	writeJSON(w, http.StatusOK, SecurityOverviewResponse{
		OpenVulnerabilitiesTotal: ov.GetOpenVulnerabilitiesTotal(),
		SeverityCounts: SeverityCounts{
			Critical:   ov.GetSeverityCounts().GetCritical(),
			High:       ov.GetSeverityCounts().GetHigh(),
			Medium:     ov.GetSeverityCounts().GetMedium(),
			Low:        ov.GetSeverityCounts().GetLow(),
			Negligible: ov.GetSeverityCounts().GetNegligible(),
		},
		ScanCoverage: ScanCoverageResponse{
			TagsTotal:   ov.GetScanCoverage().GetTagsTotal(),
			TagsScanned: ov.GetScanCoverage().GetTagsScanned(),
			Percent:     ov.GetScanCoverage().GetPercent(),
		},
		RecentScans24h:    ov.GetRecentScans_24H(),
		DaysSinceLastScan: ov.GetDaysSinceLastScan(),
	})
}
