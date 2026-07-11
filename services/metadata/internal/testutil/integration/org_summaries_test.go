//go:build integration

// Package integration — ListOrgSummaries aggregate coverage for the
// /repositories environments overview (Task 2).
//
// The test exercises the ListOrgSummaries SQL path against a real Postgres
// container (testcontainers) so the LEFT-JOIN fan-out, COUNT(DISTINCT),
// storage SUM, and the NULL → nil last_activity mapping are all validated
// end-to-end. It uses the repository directly (not the gRPC handler) —
// mirroring buildRepo-based tests such as vuln_count_test.go — because the
// aggregate is a read-only repo method with no dedicated RPC of its own here.
//
// Helper names carry an `osum` prefix because this package already defines a
// `seedManifest` (retention_eval_test.go) with a different signature; the
// prefix keeps these seed helpers collision-free within the shared package.
package integration

import (
	"context"
	"strconv"
	"testing"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// osumSeedOrg creates (or fetches) an organization under the dev tenant and
// returns its id. Thin wrapper over GetOrCreateOrganization so the test reads
// as a sequence of seed steps.
func osumSeedOrg(t *testing.T, repo *repository.Repository, tenantID, name string) string {
	t.Helper()
	orgID, err := repo.GetOrCreateOrganization(context.Background(), tenantID, name)
	if err != nil {
		t.Fatalf("osumSeedOrg(%q): %v", name, err)
	}
	return orgID
}

// osumSeedRepo creates a repository under the given org and returns its id.
func osumSeedRepo(t *testing.T, repo *repository.Repository, tenantID, orgID, name string) string {
	t.Helper()
	r, err := repo.CreateRepository(context.Background(), tenantID, orgID, name, "", false, 1<<30)
	if err != nil {
		t.Fatalf("osumSeedRepo(%q): %v", name, err)
	}
	return r.GetRepoId()
}

// osumSeedManifest pushes a single-arch image manifest whose parsed image size
// equals sizeBytes, giving the owning org both storage and a last-activity
// timestamp. PutManifest derives image_size_bytes from the manifest's
// config.size (via parseImageSize), NOT from the size_bytes argument — so the
// raw JSON carries config.size = sizeBytes to make the storage assertion exact.
func osumSeedManifest(t *testing.T, repo *repository.Repository, tenantID, repoID string, sizeBytes int64) {
	t.Helper()
	// A single-arch image manifest: no layers, config.size carries the whole
	// image size so parseImageSize returns exactly sizeBytes.
	rawJSON := []byte(`{"schemaVersion":2,"config":{"size":` + strconv.FormatInt(sizeBytes, 10) + `}}`)
	digest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if _, err := repo.PutManifest(
		context.Background(),
		tenantID,
		repoID,
		digest,
		"application/vnd.oci.image.manifest.v1+json",
		rawJSON,
		int64(len(rawJSON)),
	); err != nil {
		t.Fatalf("osumSeedManifest(size=%d): %v", sizeBytes, err)
	}
}

// TestListOrgSummaries verifies the per-org aggregate: repository count,
// summed storage, and last-activity timestamp (nil when the org has no
// manifests), ordered by org name.
func TestListOrgSummaries(t *testing.T) {
	repo := buildRepo(t)
	tenantID := devTenantID
	ctx := context.Background()

	// Seed: org "dev" with 2 repos (one with a manifest), org "prod" with 1 repo.
	devID := osumSeedOrg(t, repo, tenantID, "dev")
	prodID := osumSeedOrg(t, repo, tenantID, "prod")
	devRepo1 := osumSeedRepo(t, repo, tenantID, devID, "api")
	osumSeedRepo(t, repo, tenantID, devID, "web")        // empty repo, still counted
	osumSeedRepo(t, repo, tenantID, prodID, "api")
	osumSeedManifest(t, repo, tenantID, devRepo1, 1024)  // gives dev storage + activity

	got, err := repo.ListOrgSummaries(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListOrgSummaries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 orgs, got %d", len(got))
	}
	// ORDER BY name → dev first.
	if got[0].GetName() != "dev" || got[0].GetRepositoryCount() != 2 {
		t.Errorf("dev: name=%q repo_count=%d", got[0].GetName(), got[0].GetRepositoryCount())
	}
	if got[0].GetStorageUsedBytes() != 1024 {
		t.Errorf("dev storage: want 1024, got %d", got[0].GetStorageUsedBytes())
	}
	if got[0].GetLastActivityAt() == nil {
		t.Errorf("dev last_activity_at: want set, got nil")
	}
	if got[1].GetName() != "prod" || got[1].GetRepositoryCount() != 1 {
		t.Errorf("prod: name=%q repo_count=%d", got[1].GetName(), got[1].GetRepositoryCount())
	}
	if got[1].GetLastActivityAt() != nil {
		t.Errorf("prod last_activity_at: want nil (no manifests), got %v", got[1].GetLastActivityAt())
	}
}
