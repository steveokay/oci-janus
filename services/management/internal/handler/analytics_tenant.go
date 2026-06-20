// Package handler — analytics_tenant.go
//
// FE-API-030 — GET /api/v1/stats/analytics
//
// Tenant-wide time-series variant of FE-API-030. Same bucket sizing rules
// and metric→action mapping as the per-repo route in analytics_repo.go;
// just no RBAC scope check and no repo_id forwarding to the audit service.
//
// Authentication is the standard authMW chain — any tenant user can see
// their own tenant's aggregate counts. Unlike the per-repo route there is
// no extra scope-level grant requirement.
package handler

import (
	"log/slog"
	"net/http"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// handleGetTenantAnalytics serves GET /api/v1/stats/analytics.
// Registered from Handler.Register in handler.go.
func (h *Handler) handleGetTenantAnalytics(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	metric, action, rangeKey, rng, ok := parseAnalyticsParams(w, r)
	if !ok {
		return
	}

	resp, err := h.audit.GetAnalytics(r.Context(), &auditv1.GetAnalyticsRequest{
		TenantId:   tenantID,
		ScopeType:  "tenant",
		Action:     action,
		RangeSecs:  rng.rangeSecs,
		BucketSecs: rng.bucketSecs,
	})
	if err != nil {
		slog.Error("GetAnalytics (tenant)", "err", err, "tenant_id", tenantID, "metric", metric)
		writeError(w, http.StatusInternalServerError, "failed to fetch analytics")
		return
	}

	writeJSON(w, http.StatusOK, buildAnalyticsResponse(metric, rangeKey, rng, resp))
}
