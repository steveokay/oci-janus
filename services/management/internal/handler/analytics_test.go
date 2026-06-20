package handler_test

import (
	"net/http"
	"testing"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// resetAnalyticsCall clears the package-level fake recorder before and after
// any test that asserts on it, so cases don't leak state via the global.
func resetAnalyticsCall(t *testing.T) {
	t.Helper()
	lastAnalyticsCall = nil
	analyticsResponseOverride = nil
	t.Cleanup(func() {
		lastAnalyticsCall = nil
		analyticsResponseOverride = nil
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/analytics   (FE-API-030 — per-repo)
// ---------------------------------------------------------------------------

func TestRepoAnalytics_pulls24h_returns200WithBuckets(t *testing.T) {
	resetAnalyticsCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/analytics?metric=pulls&range=24h", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.AnalyticsResponse
	decodeJSON(t, resp, &body)

	if body.Metric != "pulls" {
		t.Errorf("Metric: got %q, want pulls", body.Metric)
	}
	if body.Range != "24h" {
		t.Errorf("Range: got %q, want 24h", body.Range)
	}
	if body.BucketSizeSecs != 3600 {
		t.Errorf("BucketSizeSecs: got %d, want 3600", body.BucketSizeSecs)
	}
	// The 24h range pre-allocates 24 buckets so quiet hours report count=0.
	if len(body.Buckets) != 24 {
		t.Errorf("expected 24 buckets, got %d", len(body.Buckets))
	}

	// The fake returns a single populated bucket of count=7 aligned to the
	// pre-allocation grid; the merged response should reflect it once.
	var nonZero int
	for _, b := range body.Buckets {
		if b.Count != 0 {
			nonZero++
		}
	}
	if nonZero != 1 {
		t.Errorf("expected exactly 1 populated bucket from the fake, got %d", nonZero)
	}
	if body.Total != 7 {
		t.Errorf("Total: got %d, want 7", body.Total)
	}

	// Audit RPC should have been called with the correct action + scope.
	if lastAnalyticsCall == nil {
		t.Fatal("expected fake GetAnalytics to be called")
	}
	if lastAnalyticsCall.action != "pull.image" {
		t.Errorf("action: got %q, want pull.image", lastAnalyticsCall.action)
	}
	if lastAnalyticsCall.scopeType != "repo" {
		t.Errorf("scopeType: got %q, want repo", lastAnalyticsCall.scopeType)
	}
	if lastAnalyticsCall.repoID != testRepoID {
		t.Errorf("repoID: got %q, want %q", lastAnalyticsCall.repoID, testRepoID)
	}
}

func TestRepoAnalytics_pushes7d_bucketSizeMatchesPreset(t *testing.T) {
	resetAnalyticsCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/analytics?metric=pushes&range=7d", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.AnalyticsResponse
	decodeJSON(t, resp, &body)
	if body.BucketSizeSecs != 6*3600 {
		t.Errorf("BucketSizeSecs: got %d, want %d (6h)", body.BucketSizeSecs, 6*3600)
	}
	if len(body.Buckets) != 28 {
		t.Errorf("expected 28 buckets for 7d/6h, got %d", len(body.Buckets))
	}
	if lastAnalyticsCall == nil || lastAnalyticsCall.action != "push.image" {
		t.Errorf("expected action=push.image, got %+v", lastAnalyticsCall)
	}
}

func TestRepoAnalytics_30d_returns30DailyBuckets(t *testing.T) {
	resetAnalyticsCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/analytics?metric=pulls&range=30d", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AnalyticsResponse
	decodeJSON(t, resp, &body)
	if body.BucketSizeSecs != 24*3600 {
		t.Errorf("BucketSizeSecs: got %d, want %d (1d)", body.BucketSizeSecs, 24*3600)
	}
	if len(body.Buckets) != 30 {
		t.Errorf("expected 30 buckets for 30d/1d, got %d", len(body.Buckets))
	}
}

func TestRepoAnalytics_invalidMetric_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/analytics?metric=cosmic&range=24h", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoAnalytics_invalidRange_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/analytics?metric=pulls&range=1y", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoAnalytics_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/unknown/analytics?metric=pulls&range=24h", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRepoAnalytics_nonMember_returns404(t *testing.T) {
	// wrong-org is not in the seeded role assignments — the route must 404
	// (not 403) so non-members cannot enumerate which orgs exist, matching
	// the FE-API-004 activity route's contract.
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/wrong-org/myrepo/analytics?metric=pulls&range=24h", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRepoAnalytics_invalidOrgName_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/INVALID/myrepo/analytics?metric=pulls&range=24h", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats/analytics   (FE-API-030 — tenant-wide)
// ---------------------------------------------------------------------------

func TestTenantAnalytics_pulls24h_returns200WithFullSeries(t *testing.T) {
	resetAnalyticsCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/analytics?metric=pulls&range=24h", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AnalyticsResponse
	decodeJSON(t, resp, &body)
	if len(body.Buckets) != 24 {
		t.Errorf("expected 24 buckets, got %d", len(body.Buckets))
	}
	if lastAnalyticsCall == nil {
		t.Fatal("expected fake GetAnalytics to be called")
	}
	if lastAnalyticsCall.scopeType != "tenant" {
		t.Errorf("scopeType: got %q, want tenant", lastAnalyticsCall.scopeType)
	}
	if lastAnalyticsCall.repoID != "" {
		t.Errorf("repoID should be empty for tenant scope, got %q", lastAnalyticsCall.repoID)
	}
}

func TestTenantAnalytics_invalidMetric_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/analytics?metric=cosmic&range=24h", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTenantAnalytics_invalidRange_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/analytics?metric=pulls&range=forever", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestTenantAnalytics_bucketAlignment seeds the fake audit server with two
// rows aligned to specific bucket boundaries and asserts the BFF places the
// counts in the expected indices of the pre-allocated grid. This is the
// critical merge-path test — a regression here would manifest as the
// dashboard sparkline showing zeros where events should appear.
func TestTenantAnalytics_bucketAlignment(t *testing.T) {
	resetAnalyticsCall(t)

	// Pick a deterministic rangeStart aligned to the BFF's preset
	// (24h / 1h). Two events: one in bucket 0, one in bucket 5.
	now := time.Now().UTC()
	rangeStart := time.Unix(((now.Unix()-24*3600)/3600)*3600, 0).UTC()
	analyticsResponseOverride = &auditv1.GetAnalyticsResponse{
		Buckets: []*auditv1.AnalyticsBucket{
			{BucketStart: timestamppb.New(rangeStart), Count: 4},
			{BucketStart: timestamppb.New(rangeStart.Add(5 * time.Hour)), Count: 9},
		},
		Total:      13,
		RangeStart: timestamppb.New(rangeStart),
	}

	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/analytics?metric=pushes&range=24h", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AnalyticsResponse
	decodeJSON(t, resp, &body)
	if len(body.Buckets) != 24 {
		t.Fatalf("expected 24 buckets, got %d", len(body.Buckets))
	}
	if body.Buckets[0].Count != 4 {
		t.Errorf("bucket[0]: got %d, want 4", body.Buckets[0].Count)
	}
	if body.Buckets[5].Count != 9 {
		t.Errorf("bucket[5]: got %d, want 9", body.Buckets[5].Count)
	}
	// Every other bucket should be zero — proves the BFF pre-allocates the
	// full grid rather than only echoing populated buckets.
	for i, b := range body.Buckets {
		if i == 0 || i == 5 {
			continue
		}
		if b.Count != 0 {
			t.Errorf("bucket[%d]: expected 0, got %d", i, b.Count)
		}
	}
	if body.Total != 13 {
		t.Errorf("Total: got %d, want 13", body.Total)
	}
}
