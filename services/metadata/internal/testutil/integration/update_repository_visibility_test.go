//go:build integration

// Package integration — real-DB coverage for UpdateRepositoryVisibility
// (Tier 2 #2). Guards the round-trip and, like the UpdateRepository test,
// that the CTE-then-SELECT resolves max_cvss_score (a RETURNING/select column
// mismatch would fail here with SQLSTATE 42703 rather than a bad value).

package integration

import (
	"context"
	"testing"
)

func TestUpdateRepositoryVisibility_RoundTrip(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "vis-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	// Create private (is_public=false), then flip to public.
	created, err := repo.CreateRepository(ctx, devTenantID, orgID, "vis-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if created.GetIsPublic() {
		t.Fatal("fixture should start private")
	}

	pub, err := repo.UpdateRepositoryVisibility(ctx, devTenantID, created.GetRepoId(), true)
	if err != nil {
		t.Fatalf("UpdateRepositoryVisibility(true): %v", err)
	}
	if !pub.GetIsPublic() {
		t.Errorf("is_public = false after flip to public, want true")
	}

	priv, err := repo.UpdateRepositoryVisibility(ctx, devTenantID, created.GetRepoId(), false)
	if err != nil {
		t.Fatalf("UpdateRepositoryVisibility(false): %v", err)
	}
	if priv.GetIsPublic() {
		t.Errorf("is_public = true after flip to private, want false")
	}
}
