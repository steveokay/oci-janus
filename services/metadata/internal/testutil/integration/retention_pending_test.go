//go:build integration

// Integration coverage for the FE-API-040 retention executor primitives on the
// metadata repository. We hit a real Postgres container via testcontainers so
// the partial index + idempotent UPDATE + interval arithmetic are all
// validated end-to-end.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// seedPendingRepoWithManifests creates an org + repo and inserts N manifests
// at controlled created_at timestamps. Returns repoID and the inserted
// manifest IDs in insertion order so the caller can refer to them by index.
func seedPendingRepoWithManifests(t *testing.T, repo *repository.Repository, count int, ageOffset time.Duration) (repoID string, ids []string) {
	t.Helper()
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "pending-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "pending-repo-"+uuid.NewString()[:8], "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	now := time.Now().UTC()
	ids = make([]string, 0, count)
	for i := 0; i < count; i++ {
		// Reuse the FE-API-038 test helper so we can set image_size_bytes
		// and a historical created_at deterministically.
		digest := "sha256:" + uuid.NewString() + uuid.NewString()
		// The helper requires a 64-char hex suffix; truncate the concat.
		digest = digest[:7+64]
		if err := repo.RawInsertManifestForTest(ctx, r.GetRepoId(), devTenantID, digest, "application/vnd.oci.image.manifest.v1+json", int64(1024*(i+1)), now.Add(-ageOffset)); err != nil {
			t.Fatalf("RawInsertManifestForTest[%d]: %v", i, err)
		}
		// Look up the inserted manifest's id (the helper does not return it).
		mid, err := fetchManifestIDForTest(ctx, repo, r.GetRepoId(), digest)
		if err != nil {
			t.Fatalf("fetchManifestIDForTest[%d]: %v", i, err)
		}
		ids = append(ids, mid)
	}
	return r.GetRepoId(), ids
}

// fetchManifestIDForTest looks up the manifest UUID by (repo_id, digest). The
// integration tests use the public repository pool indirectly — we go through
// a thin SELECT to avoid teaching the public Repository surface a test-only
// "lookup by digest" method.
func fetchManifestIDForTest(ctx context.Context, repo *repository.Repository, repoID, digest string) (string, error) {
	// Reuse GetManifest which already supports lookup by digest reference.
	// It returns a Manifest proto where ManifestId is the UUID we need.
	m, err := repo.GetManifest(ctx, devTenantID, repoID, digest)
	if err != nil {
		return "", err
	}
	return m.GetManifestId(), nil
}

// ── MarkManifestRetentionPending ─────────────────────────────────────────────

// TestMarkPending_setsColumn verifies the basic write path.
func TestMarkPending_setsColumn(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 30*24*time.Hour)
	ctx := context.Background()

	if err := repo.MarkManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("MarkManifestRetentionPending: %v", err)
	}
	// ListPendingDeleteManifests with a zero grace window should return the
	// row immediately (any non-NULL retention_pending_delete_at qualifies).
	out, err := repo.ListPendingDeleteManifests(ctx, devTenantID, 0, 10)
	if err != nil {
		t.Fatalf("ListPendingDeleteManifests: %v", err)
	}
	if len(out) != 1 || out[0].GetManifestId() != ids[0] {
		t.Errorf("expected the marked manifest, got %+v", out)
	}
}

// TestMarkPending_idempotent verifies a re-Mark preserves the original
// timestamp — the grace clock MUST NOT restart on a re-run.
func TestMarkPending_idempotent(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 30*24*time.Hour)
	ctx := context.Background()

	if err := repo.MarkManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	firstOut, err := repo.ListPendingDeleteManifests(ctx, devTenantID, 0, 10)
	if err != nil || len(firstOut) != 1 {
		t.Fatalf("first list: %v / %d rows", err, len(firstOut))
	}
	firstStamp := firstOut[0].GetPendingSince().AsTime()

	// Sleep a beat so a NOW() recompute would be visibly different.
	time.Sleep(50 * time.Millisecond)
	if err := repo.MarkManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	secondOut, err := repo.ListPendingDeleteManifests(ctx, devTenantID, 0, 10)
	if err != nil || len(secondOut) != 1 {
		t.Fatalf("second list: %v / %d rows", err, len(secondOut))
	}
	secondStamp := secondOut[0].GetPendingSince().AsTime()
	if !firstStamp.Equal(secondStamp) {
		t.Errorf("idempotent mark must preserve timestamp: first=%v second=%v", firstStamp, secondStamp)
	}
}

// TestMarkPending_unknownManifest_returnsNotFound.
func TestMarkPending_unknownManifest_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	err := repo.MarkManifestRetentionPending(ctx, devTenantID, uuid.NewString())
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── ClearManifestRetentionPending ────────────────────────────────────────────

// TestClearPending_unsetsColumn verifies the inverse round-trip.
func TestClearPending_unsetsColumn(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 30*24*time.Hour)
	ctx := context.Background()

	if err := repo.MarkManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := repo.ClearManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("clear: %v", err)
	}
	out, err := repo.ListPendingDeleteManifests(ctx, devTenantID, 0, 10)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 rows after clear, got %d", len(out))
	}
}

// ── ListPendingDeleteManifests ───────────────────────────────────────────────

// TestListPending_respectsGraceWindow verifies the past-grace filter.
func TestListPending_respectsGraceWindow(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 2, 30*24*time.Hour)
	ctx := context.Background()

	// Mark both manifests.
	for _, id := range ids {
		if err := repo.MarkManifestRetentionPending(ctx, devTenantID, id); err != nil {
			t.Fatalf("mark %s: %v", id, err)
		}
	}
	// Grace window of 1 hour — both rows were just marked so neither
	// qualifies.
	out, err := repo.ListPendingDeleteManifests(ctx, devTenantID, 3600, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("with grace > age both rows should survive, got %d", len(out))
	}
	// Grace window of 0 — both rows qualify.
	out, err = repo.ListPendingDeleteManifests(ctx, devTenantID, 0, 10)
	if err != nil {
		t.Fatalf("list zero-grace: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("with zero grace both rows should appear, got %d", len(out))
	}
}

// TestListPending_tenantIsolation verifies a populated tenant_id filters out
// other tenants. Cross-tenant scan (empty tenant_id) sees both.
//
// We seed two tenants by inserting directly via raw SQL since the test
// fixtures bootstrap only the dev tenant. The other tenant id is a fresh
// UUID — it doesn't need to exist in the tenants table because manifests
// only carries tenant_id as a denormalised column with no FK back to tenants.
func TestListPending_tenantIsolation(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	_, ids := seedPendingRepoWithManifests(t, repo, 1, 30*24*time.Hour)
	if err := repo.MarkManifestRetentionPending(ctx, devTenantID, ids[0]); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// Scan only the other tenant — should be empty.
	other := uuid.NewString()
	out, err := repo.ListPendingDeleteManifests(ctx, other, 0, 10)
	if err != nil {
		t.Fatalf("list other tenant: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("other tenant should see nothing, got %d", len(out))
	}

	// Cross-tenant scan (empty) — should see the dev-tenant row.
	out, err = repo.ListPendingDeleteManifests(ctx, "", 0, 10)
	if err != nil {
		t.Fatalf("list cross-tenant: %v", err)
	}
	if len(out) < 1 {
		t.Errorf("cross-tenant scan should see the dev-tenant row, got %d", len(out))
	}
}
