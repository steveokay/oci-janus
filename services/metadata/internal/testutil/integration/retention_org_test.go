//go:build integration

// Integration coverage for the FE-API-039 org-default + inheritance
// repository methods. We hit a real Postgres container via testcontainers
// so the JSONB round-trip, TEXT[] handling, preview_until reset semantics,
// and the per-repo→org-default fallback resolution are validated end-to-end.
//
// The fallback rules under test:
//
//   - Per-repo present → GetEffective returns it with source="repo".
//   - Per-repo absent + org default enabled → fallback to org default,
//     source="org".
//   - Per-repo absent + org default DISABLED → NotFound (a disabled default
//     deliberately does not propagate).
//   - Per-repo absent + no org default → NotFound.
//   - DELETE org default → repos that had own policies keep working;
//     repos without policies fall back to NotFound (no inheritance).
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// seedOrgRetention creates an org so the org-default upsert has a valid FK
// target. Returns the org_id (matching the existing seedRetentionRepo
// pattern but split because some FE-API-039 tests need ONLY an org row).
func seedOrgRetention(t *testing.T, repo *repository.Repository, name string) string {
	t.Helper()
	ctx := context.Background()
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, name)
	if err != nil {
		t.Fatalf("GetOrCreateOrganization(%q): %v", name, err)
	}
	return orgID
}

// seedRepoUnderOrg seeds a repo attached to an existing org so the
// inheritance resolution can traverse the org_id JOIN.
func seedRepoUnderOrg(t *testing.T, repo *repository.Repository, orgID, repoName string) string {
	t.Helper()
	ctx := context.Background()
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, repoName, "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository(%q): %v", repoName, err)
	}
	return r.GetRepoId()
}

// TestUpsertOrgRetention_freshRow_setsPreviewUntil verifies a freshly
// enabled org default receives preview_until ≈ NOW() + 24h — the same
// semantics as the per-repo upsert, since they share decidePreviewUntil.
func TestUpsertOrgRetention_freshRow_setsPreviewUntil(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-org-fresh")
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 90}}
	before := time.Now()
	got, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("UpsertOrgRetentionPolicy: %v", err)
	}
	if got.GetPreviewUntil() == nil {
		t.Fatal("expected preview_until on initial enable")
	}
	gotTS := got.GetPreviewUntil().AsTime()
	expectedMin := before.Add(24 * time.Hour).Add(-time.Minute)
	expectedMax := time.Now().Add(24 * time.Hour).Add(time.Minute)
	if gotTS.Before(expectedMin) || gotTS.After(expectedMax) {
		t.Errorf("preview_until out of range: got %v", gotTS)
	}
	if got.GetOrgId() != orgID {
		t.Errorf("OrgId round-trip: got %q, want %q", got.GetOrgId(), orgID)
	}
}

// TestUpsertOrgRetention_reUpsertSameRules_preservesPreviewUntil verifies
// the shared preview-window helper preserves preview_until on a no-op
// re-save (otherwise enforcement would never start).
func TestUpsertOrgRetention_reUpsertSameRules_preservesPreviewUntil(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-org-reupsert")
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 90}}
	first, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstPreview := first.GetPreviewUntil().AsTime()

	time.Sleep(50 * time.Millisecond)
	second, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !first.GetPreviewUntil().AsTime().Equal(second.GetPreviewUntil().AsTime()) {
		t.Errorf("preview_until should be preserved when rules unchanged: got %v then %v",
			firstPreview, second.GetPreviewUntil().AsTime())
	}
}

// TestDeleteOrgRetention_happyPath_thenNotFound verifies the row goes
// away cleanly and a subsequent Get surfaces ErrNotFound.
func TestDeleteOrgRetention_happyPath_thenNotFound(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-org-delete")
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	if _, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true, rules, []string{"latest"}, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.DeleteOrgRetentionPolicy(ctx, devTenantID, orgID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetOrgRetentionPolicy(ctx, devTenantID, orgID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestDeleteOrgRetention_missing_returnsNotFound — idempotent semantics
// are intentionally not applied; callers expect to know.
func TestDeleteOrgRetention_missing_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-org-missing-del")
	ctx := context.Background()
	if err := repo.DeleteOrgRetentionPolicy(ctx, devTenantID, orgID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ── Inheritance resolution ────────────────────────────────────────────────

// TestGetEffective_repoHit_winsOverOrgDefault verifies a per-repo policy
// is authoritative even when an enabled org default also exists.
func TestGetEffective_repoHit_winsOverOrgDefault(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-eff-repohit")
	repoID := seedRepoUnderOrg(t, repo, orgID, "repohit")
	ctx := context.Background()

	// Seed an enabled org default — the per-repo policy should still win.
	if _, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 365}},
		[]string{"latest"}, ""); err != nil {
		t.Fatalf("seed org default: %v", err)
	}
	// Seed a per-repo policy that's clearly different (value 30 vs 365).
	if _, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		[]string{"latest"}, ""); err != nil {
		t.Fatalf("seed repo policy: %v", err)
	}

	eff, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, repoID)
	if err != nil {
		t.Fatalf("GetEffective: %v", err)
	}
	if eff.InheritedFrom != "repo" {
		t.Errorf("InheritedFrom: got %q, want repo", eff.InheritedFrom)
	}
	if len(eff.Policy.GetRules()) == 0 || eff.Policy.GetRules()[0].GetValue() != 30 {
		t.Errorf("expected per-repo rule value 30, got %+v", eff.Policy.GetRules())
	}
}

// TestGetEffective_orgFallback_whenEnabled verifies the fallback path
// returns the org default with source="org" when no per-repo policy exists.
func TestGetEffective_orgFallback_whenEnabled(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-eff-orgfall")
	repoID := seedRepoUnderOrg(t, repo, orgID, "fallrepo")
	ctx := context.Background()

	if _, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 365}},
		[]string{"latest"}, ""); err != nil {
		t.Fatalf("seed org default: %v", err)
	}

	eff, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, repoID)
	if err != nil {
		t.Fatalf("GetEffective: %v", err)
	}
	if eff.InheritedFrom != "org" {
		t.Errorf("InheritedFrom: got %q, want org", eff.InheritedFrom)
	}
	if eff.OrgID != orgID {
		t.Errorf("OrgID: got %q, want %q", eff.OrgID, orgID)
	}
	if eff.Policy.GetOrgId() != orgID {
		t.Errorf("policy.org_id: got %q, want %q", eff.Policy.GetOrgId(), orgID)
	}
}

// TestGetEffective_disabledOrgDefault_returnsNotFound verifies the
// "disabled default does not propagate" rule. The repo has no per-repo
// policy and the org has a disabled default — the result is NotFound,
// NOT a fallback to the disabled row.
func TestGetEffective_disabledOrgDefault_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-eff-disabled")
	repoID := seedRepoUnderOrg(t, repo, orgID, "disabledrepo")
	ctx := context.Background()

	// Seed a DISABLED org default. enabled=false → rules can be empty.
	if _, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, false,
		nil, []string{"latest"}, ""); err != nil {
		t.Fatalf("seed disabled org default: %v", err)
	}

	_, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, repoID)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound for disabled org default, got %v", err)
	}
}

// TestGetEffective_noPolicyAnywhere_returnsNotFound verifies the empty
// state — neither per-repo nor org default — surfaces as NotFound.
func TestGetEffective_noPolicyAnywhere_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-eff-empty")
	repoID := seedRepoUnderOrg(t, repo, orgID, "emptyrepo")
	ctx := context.Background()

	_, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, repoID)
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound when neither layer has a row, got %v", err)
	}
}

// TestGetEffective_deleteOrgDefault_repoWithPolicyStillWorks verifies the
// per-repo path is unaffected by org-default deletion — a repo with its
// own policy keeps returning that policy, while the sibling repo without
// its own policy falls back to NotFound after the delete.
func TestGetEffective_deleteOrgDefault_repoWithPolicyStillWorks(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-eff-mix")
	withPolicy := seedRepoUnderOrg(t, repo, orgID, "withpolicy")
	withoutPolicy := seedRepoUnderOrg(t, repo, orgID, "withoutpolicy")
	ctx := context.Background()

	// Seed both the org default and one per-repo policy.
	if _, err := repo.UpsertOrgRetentionPolicy(ctx, devTenantID, orgID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 365}},
		[]string{"latest"}, ""); err != nil {
		t.Fatalf("seed org default: %v", err)
	}
	if _, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, withPolicy, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		[]string{"latest"}, ""); err != nil {
		t.Fatalf("seed per-repo policy: %v", err)
	}

	// Drop the org default.
	if err := repo.DeleteOrgRetentionPolicy(ctx, devTenantID, orgID); err != nil {
		t.Fatalf("delete org default: %v", err)
	}

	// withPolicy repo still resolves to its own policy.
	eff, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, withPolicy)
	if err != nil {
		t.Fatalf("withPolicy GetEffective: %v", err)
	}
	if eff.InheritedFrom != "repo" {
		t.Errorf("withPolicy InheritedFrom: got %q, want repo", eff.InheritedFrom)
	}

	// withoutPolicy repo now resolves to NotFound — fallback is gone.
	if _, err := repo.GetEffectiveRetentionPolicy(ctx, devTenantID, withoutPolicy); err != repository.ErrNotFound {
		t.Errorf("withoutPolicy: expected ErrNotFound after org-default delete, got %v", err)
	}
}

// TestLookupOrgIDByName_happyPath verifies the read-only lookup returns
// the existing org's UUID. This is the read-side counterpart to
// GetOrCreateOrganization that the BFF uses to translate org-name URLs
// without an unintended insert.
func TestLookupOrgIDByName_happyPath(t *testing.T) {
	repo := buildRepo(t)
	orgID := seedOrgRetention(t, repo, "fe039-lookup")
	ctx := context.Background()
	got, err := repo.LookupOrgIDByName(ctx, devTenantID, "fe039-lookup")
	if err != nil {
		t.Fatalf("LookupOrgIDByName: %v", err)
	}
	if got != orgID {
		t.Errorf("org_id: got %q, want %q", got, orgID)
	}
}

// TestLookupOrgIDByName_missing_returnsNotFound verifies the read-only
// path does NOT create the org row (unlike GetOrCreateOrganization).
func TestLookupOrgIDByName_missing_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	_, err := repo.LookupOrgIDByName(ctx, devTenantID, "fe039-no-such-org")
	if err != repository.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
