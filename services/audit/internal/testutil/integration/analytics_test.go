//go:build integration

// FE-API-030 — integration tests for repository.GetAnalytics.
//
// Exercises the date_bin grouping against a real PostgreSQL 16 container so
// we cover the PG14+ function the production code depends on. The fake
// driver in unit tests would miss any SQL-level bug.
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// seedAnalyticsRow inserts one push.image audit event with a metadata.raw.repo_id
// populated so the repo-scoped query branch can match it. The unit-test seed in
// repo_activity_test.go writes repository_name but not repo_id; analytics
// matches on repo_id (same convention as GetBuildHistory) so we need both
// fields here.
func seedAnalyticsRow(t *testing.T, repo *repository.Repository, tenant uuid.UUID, action, repoID string, when time.Time) {
	t.Helper()
	rawBytes, err := json.Marshal(map[string]any{
		"repository_name": "myorg/myrepo",
		"repo_id":         repoID,
		"tag":             "v1",
		"manifest_digest": "sha256:111",
	})
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	wrapped, err := json.Marshal(map[string]any{
		"event_id": uuid.New().String(),
		"raw":      json.RawMessage(rawBytes),
	})
	if err != nil {
		t.Fatalf("marshal wrapped: %v", err)
	}
	if err := repo.Insert(context.Background(), &repository.AuditEvent{
		TenantID:   tenant,
		ActorID:    "alice",
		ActorType:  "user",
		Action:     action,
		Resource:   "myorg/myrepo:v1",
		Outcome:    "success",
		Metadata:   wrapped,
		OccurredAt: when,
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

func TestGetAnalytics_tenantScope_groupsByBucket(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()
	repoID := uuid.New().String()

	// Align our reference instant to the top of an hour so the seeded
	// timestamps fall cleanly into the 1-hour buckets we ask date_bin to
	// build.
	rangeStart := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

	// 2 events in bucket [start, start+1h); 3 events in bucket [start+1h, start+2h);
	// 0 events in the trailing bucket — exercises sparse, zero-skipping output.
	for _, off := range []time.Duration{0, 5 * time.Minute} {
		seedAnalyticsRow(t, repo, tenant, "push.image", repoID, rangeStart.Add(off))
	}
	for _, off := range []time.Duration{time.Hour, time.Hour + time.Minute, time.Hour + 30*time.Minute} {
		seedAnalyticsRow(t, repo, tenant, "push.image", repoID, rangeStart.Add(off))
	}

	rangeEnd := rangeStart.Add(3 * time.Hour)
	rows, err := repo.GetAnalytics(
		context.Background(),
		tenant,
		repository.AnalyticsScope{TenantWide: true},
		"push.image",
		rangeStart,
		rangeEnd,
		3600,
	)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 populated buckets (sparse third skipped), got %d", len(rows))
	}

	// Buckets are ordered ASC and aligned to rangeStart.
	if !rows[0].BucketStart.Equal(rangeStart) {
		t.Errorf("expected bucket[0]=rangeStart, got %v", rows[0].BucketStart)
	}
	if rows[0].Count != 2 {
		t.Errorf("expected bucket[0] count=2, got %d", rows[0].Count)
	}
	if !rows[1].BucketStart.Equal(rangeStart.Add(time.Hour)) {
		t.Errorf("expected bucket[1]=rangeStart+1h, got %v", rows[1].BucketStart)
	}
	if rows[1].Count != 3 {
		t.Errorf("expected bucket[1] count=3, got %d", rows[1].Count)
	}
}

func TestGetAnalytics_repoScope_filtersByRepoID(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()
	wantedRepoID := uuid.New().String()
	otherRepoID := uuid.New().String()

	rangeStart := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	// 1 event for the target repo, 2 events for a sibling that must not leak
	// into the count.
	seedAnalyticsRow(t, repo, tenant, "push.image", wantedRepoID, rangeStart.Add(5*time.Minute))
	seedAnalyticsRow(t, repo, tenant, "push.image", otherRepoID, rangeStart.Add(6*time.Minute))
	seedAnalyticsRow(t, repo, tenant, "push.image", otherRepoID, rangeStart.Add(7*time.Minute))

	rows, err := repo.GetAnalytics(
		context.Background(),
		tenant,
		repository.AnalyticsScope{TenantWide: false, RepoID: wantedRepoID},
		"push.image",
		rangeStart,
		rangeStart.Add(time.Hour),
		3600,
	)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(rows))
	}
	if rows[0].Count != 1 {
		t.Errorf("expected count=1 for target repo, got %d", rows[0].Count)
	}
}

func TestGetAnalytics_tenantScope_isolatesAcrossTenants(t *testing.T) {
	repo := newRepo(t)
	tenantA := uuid.New()
	tenantB := uuid.New()

	rangeStart := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	seedAnalyticsRow(t, repo, tenantA, "push.image", uuid.New().String(), rangeStart.Add(10*time.Minute))
	seedAnalyticsRow(t, repo, tenantB, "push.image", uuid.New().String(), rangeStart.Add(20*time.Minute))

	rows, err := repo.GetAnalytics(
		context.Background(),
		tenantA,
		repository.AnalyticsScope{TenantWide: true},
		"push.image",
		rangeStart,
		rangeStart.Add(time.Hour),
		3600,
	)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if len(rows) != 1 || rows[0].Count != 1 {
		t.Errorf("expected tenantA to see exactly its own event, got %+v", rows)
	}
}

func TestGetAnalytics_emptyWindow_returnsNoRows(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()

	rangeStart := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	rows, err := repo.GetAnalytics(
		context.Background(),
		tenant,
		repository.AnalyticsScope{TenantWide: true},
		"push.image",
		rangeStart,
		rangeStart.Add(time.Hour),
		3600,
	)
	if err != nil {
		t.Fatalf("GetAnalytics: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows on empty window, got %d", len(rows))
	}
}
