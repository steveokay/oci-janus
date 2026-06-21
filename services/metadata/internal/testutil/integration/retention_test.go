//go:build integration

// Integration coverage for the FE-API-037 retention policy repository
// methods. We hit a real Postgres container via testcontainers so the JSONB
// round-trip, TEXT[] handling, and preview_until reset semantics are
// validated end-to-end.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// seedRetentionRepo seeds an org + repo so the policy upsert has a valid FK
// target. Returns the repo_id so tests can build requests against it.
func seedRetentionRepo(t *testing.T, repo *repository.Repository) string {
	t.Helper()
	ctx := context.Background()
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "retention-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "retention-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	return r.GetRepoId()
}

// TestUpsertRetention_freshRow_setsPreviewUntil verifies that a freshly
// inserted enabled policy receives preview_until ≈ NOW() + 24h.
func TestUpsertRetention_freshRow_setsPreviewUntil(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	before := time.Now()
	got, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("UpsertRepoRetentionPolicy: %v", err)
	}
	if got.GetPreviewUntil() == nil {
		t.Fatal("expected preview_until to be set on initial enable")
	}
	gotTS := got.GetPreviewUntil().AsTime()
	expectedMin := before.Add(24 * time.Hour).Add(-time.Minute)
	expectedMax := time.Now().Add(24 * time.Hour).Add(time.Minute)
	if gotTS.Before(expectedMin) || gotTS.After(expectedMax) {
		t.Errorf("preview_until out of range: got %v, want roughly NOW+24h", gotTS)
	}
}

// TestUpsertRetention_reUpsertSameRules_preservesPreviewUntil verifies that
// a no-op re-save (same enabled, same rules) doesn't restart the preview window.
func TestUpsertRetention_reUpsertSameRules_preservesPreviewUntil(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	first, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstPreview := first.GetPreviewUntil().AsTime()

	// Sleep a beat so a fresh NOW() would be visibly different if the impl
	// mistakenly recomputed preview_until.
	time.Sleep(50 * time.Millisecond)
	second, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	secondPreview := second.GetPreviewUntil().AsTime()
	if !firstPreview.Equal(secondPreview) {
		t.Errorf("preview_until should be preserved when rules unchanged: got %v then %v", firstPreview, secondPreview)
	}
}

// TestUpsertRetention_rulesChangeMaterially_resetsPreviewUntil verifies the
// "rules changed" path resets the preview window.
func TestUpsertRetention_rulesChangeMaterially_resetsPreviewUntil(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	first, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		[]string{"latest"}, "")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstPreview := first.GetPreviewUntil().AsTime()

	time.Sleep(50 * time.Millisecond)
	second, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 60}}, // value differs
		[]string{"latest"}, "")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	secondPreview := second.GetPreviewUntil().AsTime()
	if !secondPreview.After(firstPreview) {
		t.Errorf("expected preview_until to advance after rule change: first %v, second %v", firstPreview, secondPreview)
	}
}

// TestUpsertRetention_disableThenEnable_restartsPreview verifies the
// enabled=false → enabled=true transition resets preview_until.
func TestUpsertRetention_disableThenEnable_restartsPreview(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	_, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	disabled, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, false, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.GetPreviewUntil() != nil {
		t.Errorf("preview_until should be cleared on disable, got %v", disabled.GetPreviewUntil())
	}
	time.Sleep(50 * time.Millisecond)
	reenabled, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if reenabled.GetPreviewUntil() == nil {
		t.Error("preview_until should be set again after re-enable")
	}
}

// TestUpsertRetention_reUpsertPreservesCreatedAt verifies that updating a
// policy does not touch created_at — only updated_at should advance.
func TestUpsertRetention_reUpsertPreservesCreatedAt(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	first, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, "")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	firstCreated := first.GetCreatedAt().AsTime()
	firstUpdated := first.GetUpdatedAt().AsTime()

	time.Sleep(50 * time.Millisecond)
	second, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true,
		[]*metadatav1.RetentionRule{{Kind: "max_count", Value: 5}}, // different rule
		[]string{"latest"}, "")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !second.GetCreatedAt().AsTime().Equal(firstCreated) {
		t.Errorf("created_at should not change on update: first %v, second %v", firstCreated, second.GetCreatedAt().AsTime())
	}
	if !second.GetUpdatedAt().AsTime().After(firstUpdated) {
		t.Errorf("updated_at should advance on update: first %v, second %v", firstUpdated, second.GetUpdatedAt().AsTime())
	}
}

// TestGetRetention_roundTrip verifies a written policy can be read back.
func TestGetRetention_roundTrip(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{
		{Kind: "max_age_days", Value: 30},
		{Kind: "dangling_grace_days", Value: 7},
	}
	patterns := []string{"latest", "stable", `^v?\d+(\.\d+){0,2}$`}
	if _, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, patterns, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.GetRepoRetentionPolicy(ctx, devTenantID, repoID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.GetRules()) != 2 {
		t.Errorf("got %d rules, want 2", len(got.GetRules()))
	}
	if len(got.GetProtectedTagPatterns()) != 3 {
		t.Errorf("got %d patterns, want 3", len(got.GetProtectedTagPatterns()))
	}
}

// TestGetRetention_missingRow_returnsNotFound covers the FE-API-039 fallback
// trigger: the BFF needs a clean NotFound to know to drop down to org default.
func TestGetRetention_missingRow_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	_, err := repo.GetRepoRetentionPolicy(ctx, devTenantID, repoID)
	if err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestDeleteRetention_happyPath_thenNotFound verifies a delete clears the row
// so a subsequent Get returns NotFound.
func TestDeleteRetention_happyPath_thenNotFound(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()

	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	if _, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, repoID, true, rules, []string{"latest"}, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.DeleteRepoRetentionPolicy(ctx, devTenantID, repoID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetRepoRetentionPolicy(ctx, devTenantID, repoID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestDeleteRetention_missing_returnsNotFound.
func TestDeleteRetention_missing_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionRepo(t, repo)
	ctx := context.Background()
	if err := repo.DeleteRepoRetentionPolicy(ctx, devTenantID, repoID); err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestUpsertRetention_repoFKViolation_returnsNotFound exercises the
// "repo deleted out from under us" path — the upsert should surface
// ErrNotFound for the 23503 foreign-key error.
func TestUpsertRetention_repoFKViolation_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	bogusRepoID := "00000000-0000-0000-0000-deadbeefdead"
	rules := []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}}
	_, err := repo.UpsertRepoRetentionPolicy(ctx, devTenantID, bogusRepoID, true, rules, []string{"latest"}, "")
	if err != repository.ErrNotFound {
		t.Fatalf("expected ErrNotFound for FK violation, got %v", err)
	}
}
