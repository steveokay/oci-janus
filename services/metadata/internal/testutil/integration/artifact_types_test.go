//go:build integration

package integration

import (
	"context"
	"sort"
	"testing"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// atypeSeedManifest pushes a manifest whose config.mediaType drives
// deriveArtifactType, so the owning repo derives the matching artifact type.
func atypeSeedManifest(t *testing.T, repo *repository.Repository, tenantID, repoID, digest, configMediaType string) {
	t.Helper()
	rawJSON := []byte(`{"schemaVersion":2,"config":{"mediaType":"` + configMediaType + `","size":1}}`)
	if _, err := repo.PutManifest(context.Background(), tenantID, repoID, digest,
		"application/vnd.oci.image.manifest.v1+json", rawJSON, int64(len(rawJSON))); err != nil {
		t.Fatalf("seed manifest (%s): %v", configMediaType, err)
	}
}

func TestListRepositories_artifactTypes(t *testing.T) {
	repo := buildRepo(t)
	tenantID := devTenantID
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, tenantID, "atypes")
	if err != nil {
		t.Fatalf("org: %v", err)
	}

	imgRepo, err := repo.CreateRepository(ctx, tenantID, orgID, "img", "", false, 1<<30)
	if err != nil {
		t.Fatalf("create img: %v", err)
	}
	helmRepo, err := repo.CreateRepository(ctx, tenantID, orgID, "chart", "", false, 1<<30)
	if err != nil {
		t.Fatalf("create chart: %v", err)
	}
	mixedRepo, err := repo.CreateRepository(ctx, tenantID, orgID, "mixed", "", false, 1<<30)
	if err != nil {
		t.Fatalf("create mixed: %v", err)
	}

	const imgCfg = "application/vnd.oci.image.config.v1+json"
	const helmCfg = "application/vnd.cncf.helm.config.v1+json"
	atypeSeedManifest(t, repo, tenantID, imgRepo.GetRepoId(),
		"sha256:1111111111111111111111111111111111111111111111111111111111111111", imgCfg)
	atypeSeedManifest(t, repo, tenantID, helmRepo.GetRepoId(),
		"sha256:2222222222222222222222222222222222222222222222222222222222222222", helmCfg)
	atypeSeedManifest(t, repo, tenantID, mixedRepo.GetRepoId(),
		"sha256:3333333333333333333333333333333333333333333333333333333333333333", imgCfg)
	atypeSeedManifest(t, repo, tenantID, mixedRepo.GetRepoId(),
		"sha256:4444444444444444444444444444444444444444444444444444444444444444", helmCfg)

	repos, err := repo.ListRepositories(ctx, tenantID, orgID, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	got := map[string][]string{}
	for _, r := range repos {
		ats := append([]string(nil), r.GetArtifactTypes()...)
		sort.Strings(ats)
		got[r.GetName()] = ats
	}
	assertEq := func(name string, want []string) {
		if len(got[name]) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got[name], want)
		}
		for i := range want {
			if got[name][i] != want[i] {
				t.Errorf("%s: got %v want %v", name, got[name], want)
				return
			}
		}
	}
	assertEq("img", []string{"image"})
	assertEq("chart", []string{"helm"})
	assertEq("mixed", []string{"helm", "image"})
}
