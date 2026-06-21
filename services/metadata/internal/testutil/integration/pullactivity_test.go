//go:build integration

// FE-API-042 — integration coverage for the manifests.last_pulled_at column
// and the 24h-debounced UpsertManifestLastPulledAt path. We hit a real
// Postgres container via testcontainers so the column type, the interval
// arithmetic, and the tenant_id WHERE clause are all validated end-to-end.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUpsertManifestLastPulledAt_freshRowGetsUpdated verifies the basic
// happy path: a manifest whose last_pulled_at is NULL is updated.
func TestUpsertManifestLastPulledAt_freshRowGetsUpdated(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 0)
	ctx := context.Background()

	now := time.Now().UTC()
	rows, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], devTenantID, now)
	if err != nil {
		t.Fatalf("UpsertManifestLastPulledAt: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows updated: got %d, want 1", rows)
	}
}

// TestUpsertManifestLastPulledAt_within24h_isNoOp confirms the debounce —
// a second call within the 24h window must NOT touch the row. The whole
// point of debouncing is to keep hot-manifest write amplification bounded.
func TestUpsertManifestLastPulledAt_within24h_isNoOp(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 0)
	ctx := context.Background()

	first := time.Now().UTC()
	if _, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], devTenantID, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Second call ~10 minutes later — well inside the 24h debounce window.
	second := first.Add(10 * time.Minute)
	rows, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], devTenantID, second)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if rows != 0 {
		t.Errorf("debounce broken: second call within 24h updated %d rows, want 0", rows)
	}
}

// TestUpsertManifestLastPulledAt_after24h_updatesAgain confirms the debounce
// releases after the configured 24h window. We pre-stamp last_pulled_at to
// a value 25h in the past (rather than waiting 24h) so the test is fast.
func TestUpsertManifestLastPulledAt_after24h_updatesAgain(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 0)
	ctx := context.Background()

	stale := time.Now().UTC().Add(-25 * time.Hour)
	if _, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], devTenantID, stale); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}

	rows, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], devTenantID, time.Now().UTC())
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if rows != 1 {
		t.Errorf("post-24h re-upsert updated %d rows, want 1", rows)
	}
}

// TestUpsertManifestLastPulledAt_wrongTenant_doesNotUpdate verifies the
// tenant isolation guard on the WHERE clause. A poisoned event from one
// tenant must not stamp another tenant's manifest, even if it guesses the
// manifest UUID correctly.
func TestUpsertManifestLastPulledAt_wrongTenant_doesNotUpdate(t *testing.T) {
	repo := buildRepo(t)
	_, ids := seedPendingRepoWithManifests(t, repo, 1, 0)
	ctx := context.Background()

	otherTenant := uuid.NewString()
	rows, err := repo.UpsertManifestLastPulledAt(ctx, ids[0], otherTenant, time.Now().UTC())
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if rows != 0 {
		t.Errorf("cross-tenant write succeeded — got %d rows, want 0", rows)
	}
}

// TestFindManifestIDByDigest_roundtrip exercises the fallback path the
// consumer uses when the event payload lacks ManifestID. We pull the digest
// off the seeded manifest via ListUntaggedManifests (the seed helper does
// not expose digests directly) and then resolve back to the UUID.
func TestFindManifestIDByDigest_roundtrip(t *testing.T) {
	repo := buildRepo(t)
	repoID, ids := seedPendingRepoWithManifests(t, repo, 1, 0)
	ctx := context.Background()

	manifests, err := repo.ListUntaggedManifests(ctx, devTenantID, repoID)
	if err != nil {
		t.Fatalf("ListUntaggedManifests: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("expected 1 untagged manifest, got %d", len(manifests))
	}

	got, err := repo.FindManifestIDByDigest(ctx, devTenantID, repoID, manifests[0].GetDigest())
	if err != nil {
		t.Fatalf("FindManifestIDByDigest: %v", err)
	}
	if got != ids[0] {
		t.Errorf("resolved manifest id: got %q, want %q", got, ids[0])
	}
}
