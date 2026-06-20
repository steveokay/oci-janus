// Package handler — analytics_repo.go
//
// FE-API-030 — GET /api/v1/repositories/{org}/{repo}/analytics
//
// Returns a time-series of pull or push counts for a single repository so
// the dashboard can render the per-repo sparkline. The route mirrors the
// tenant-wide variant in analytics_tenant.go — both share the bucket-sizing
// rules, metric→action mapping, and pre-allocation helper defined here.
//
// Authorization mirrors the existing repo-detail routes (FE-API-004 etc.):
// caller must hold at least the reader role on the repo or its parent org.
package handler

import (
	"log/slog"
	"net/http"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// AnalyticsBucketResponse is the JSON wire form of one populated bucket.
// bucket_start is RFC3339 UTC so the dashboard can plot directly without
// timezone fiddling.
type AnalyticsBucketResponse struct {
	BucketStart time.Time `json:"bucket_start"`
	Count       int64     `json:"count"`
}

// AnalyticsResponse is the top-level JSON envelope returned by both the
// per-repo and tenant-wide analytics routes. Buckets is always a
// contiguous, zero-filled, ascending-time series — quiet windows report
// count=0 rather than absent, so the sparkline component can iterate the
// array directly without gap-filling on the client.
type AnalyticsResponse struct {
	Metric         string                    `json:"metric"`
	Range          string                    `json:"range"`
	BucketSizeSecs int64                     `json:"bucket_size_secs"`
	Buckets        []AnalyticsBucketResponse `json:"buckets"`
	Total          int64                     `json:"total"`
}

// analyticsRange is the set of caller-friendly ranges the dashboard surfaces
// through the analytics routes. Picking the bucket size on the BFF (rather
// than the audit service) keeps the gRPC API generic and lets the dashboard
// add a new range (say "90d" / 1-day buckets) without touching the audit
// service.
//
// Bucket sizing rationale (per FE-API-030):
//   - 24h → 1h  buckets → 24 points  (interactive sparkline; hour granularity)
//   - 7d  → 6h  buckets → 28 points  (week view stays under 30 points)
//   - 30d → 1d  buckets → 30 points  (month view stays under 30 points)
//
// All ranges produce <=30 points, which is roughly the maximum a 200px
// sparkline can render legibly.
type analyticsRange struct {
	rangeSecs  int64
	bucketSecs int64
	numBuckets int
}

var analyticsRanges = map[string]analyticsRange{
	"24h": {rangeSecs: 24 * 3600, bucketSecs: 3600, numBuckets: 24},
	"7d":  {rangeSecs: 7 * 24 * 3600, bucketSecs: 6 * 3600, numBuckets: 28},
	"30d": {rangeSecs: 30 * 24 * 3600, bucketSecs: 24 * 3600, numBuckets: 30},
}

// allowedAnalyticsMetrics maps the caller-friendly ?metric= values to the
// canonical audit action strings the audit service recognises. Only "pulls"
// and "pushes" are exposed — adding more metrics is a deliberate decision
// because each one needs a matching audit_events action to count.
//
// IMPORTANT: pull.image is NOT currently produced by the audit
// eventconsumer (services/audit/internal/eventconsumer/consumer.go). The
// route still works correctly — the query returns zero rows and the
// pre-allocated grid reports count=0 for every bucket. Document the gap
// here so a future implementer wiring pull events to RabbitMQ knows what
// to publish.
var allowedAnalyticsMetrics = map[string]string{
	"pulls":  "pull.image",
	"pushes": "push.image",
}

// handleGetRepoAnalytics serves GET /api/v1/repositories/{org}/{repo}/analytics.
// Registered from Handler.Register in handler.go.
func (h *Handler) handleGetRepoAnalytics(w http.ResponseWriter, r *http.Request) {
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

	// Parse metric + range BEFORE the RBAC / lookup checks so callers who
	// fat-finger ?metric=foo on a repo they CAN see get the 400, not the
	// 403/404 — keeps the error model consistent with FE-API-004.
	metric, action, rangeKey, rng, ok := parseAnalyticsParams(w, r)
	if !ok {
		return
	}

	// PENTEST-006 parity with /tags, /scan, /activity: caller needs at least
	// reader on the repo (org-scoped grant counts via the containment rule).
	// Return 404 — not 403 — so non-members cannot enumerate which repos
	// exist.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Resolve the repo so we have its UUID for the audit GetAnalytics call
	// (the audit service matches on metadata.raw.repo_id — same convention
	// GetBuildHistory uses to avoid a LIKE scan over the resource column).
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	resp, err := h.audit.GetAnalytics(r.Context(), &auditv1.GetAnalyticsRequest{
		TenantId:   tenantID,
		ScopeType:  "repo",
		RepoId:     repo.GetRepoId(),
		Action:     action,
		RangeSecs:  rng.rangeSecs,
		BucketSecs: rng.bucketSecs,
	})
	if err != nil {
		slog.Error("GetAnalytics (repo)", "err", err, "repo", org+"/"+repoName, "metric", metric)
		writeError(w, http.StatusInternalServerError, "failed to fetch analytics")
		return
	}

	writeJSON(w, http.StatusOK, buildAnalyticsResponse(metric, rangeKey, rng, resp))
}

// parseAnalyticsParams pulls and validates the metric + range query params.
// On error it writes a 400 response and returns ok=false; callers should
// return without touching the writer further.
func parseAnalyticsParams(w http.ResponseWriter, r *http.Request) (metric, action, rangeKey string, rng analyticsRange, ok bool) {
	q := r.URL.Query()
	metric = q.Get("metric")
	action, mok := allowedAnalyticsMetrics[metric]
	if !mok {
		writeError(w, http.StatusBadRequest, "metric must be one of: pulls, pushes")
		return "", "", "", analyticsRange{}, false
	}

	rangeKey = q.Get("range")
	rng, rok := analyticsRanges[rangeKey]
	if !rok {
		writeError(w, http.StatusBadRequest, "range must be one of: 24h, 7d, 30d")
		return "", "", "", analyticsRange{}, false
	}

	return metric, action, rangeKey, rng, true
}

// buildAnalyticsResponse pre-allocates the empty contiguous bucket series
// and merges the populated rows from the audit response into it. The audit
// service intentionally omits zero-count buckets (keeps the gRPC payload
// small), and the dashboard sparkline needs a contiguous array — so the
// merge happens here on the BFF.
//
// Bucket boundaries are derived from the audit response's range_start so
// they match exactly what date_bin produced, even if the BFF's clock has
// drifted a sub-second from the audit pod's.
func buildAnalyticsResponse(metric, rangeKey string, rng analyticsRange, resp *auditv1.GetAnalyticsResponse) AnalyticsResponse {
	rangeStart := resp.GetRangeStart().AsTime().UTC()
	bucketDur := time.Duration(rng.bucketSecs) * time.Second

	// Pre-allocate the full grid with zero counts. We key by Unix-second
	// alignment which is exact for any bucket >= 1s and avoids string
	// formatting in the hot path.
	buckets := make([]AnalyticsBucketResponse, rng.numBuckets)
	indexByStart := make(map[int64]int, rng.numBuckets)
	for i := 0; i < rng.numBuckets; i++ {
		t := rangeStart.Add(time.Duration(i) * bucketDur)
		buckets[i] = AnalyticsBucketResponse{BucketStart: t, Count: 0}
		indexByStart[t.Unix()] = i
	}

	// Merge in the populated rows. Buckets returned outside the expected
	// grid (which would only happen if the audit service drifted its bucket
	// alignment) are silently dropped — the BFF's grid is the source of
	// truth for the response.
	for _, b := range resp.GetBuckets() {
		startUnix := b.GetBucketStart().AsTime().UTC().Unix()
		if idx, ok := indexByStart[startUnix]; ok {
			buckets[idx].Count = b.GetCount()
		}
	}

	return AnalyticsResponse{
		Metric:         metric,
		Range:          rangeKey,
		BucketSizeSecs: rng.bucketSecs,
		Buckets:        buckets,
		Total:          resp.GetTotal(),
	}
}
