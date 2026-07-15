//go:build integration

// Package integration — real-DB coverage for UpdateRepository.
//
// This pins a regression: UpdateRepository's CTE `RETURNING` clause omitted
// max_cvss_score (added by migration 00019), while the outer SELECT applies
// repoSelectCols which references `r.max_cvss_score`. Because the CTE alias
// `r` (= the `updated` CTE) lacked that column, every UpdateRepository call
// failed with `column r.max_cvss_score does not exist`. The fake-repo unit
// tests can't catch this — only a real query against the real schema does.

package integration

import (
	"context"
	"testing"
)

func TestUpdateRepository_RoundTrip_ReturnsFullRow(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "upd-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	created, err := repo.CreateRepository(ctx, devTenantID, orgID, "upd-repo", "initial description", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	// The failing call before the fix — the RETURNING/select column mismatch
	// surfaced here as a query error rather than a bad value.
	updated, err := repo.UpdateRepository(ctx, devTenantID, created.GetRepoId(), "new description")
	if err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}
	if got := updated.GetDescription(); got != "new description" {
		t.Errorf("description = %q, want %q", got, "new description")
	}
	// A fresh repo has no CVSS gate — the column round-trips as an unset
	// (nil) wrapper. The assertion's real job is proving the SELECT over the
	// updated CTE resolves max_cvss_score at all.
	if updated.GetMaxCvssScore() != nil {
		t.Errorf("max_cvss_score = %v, want nil for a fresh repo", updated.GetMaxCvssScore())
	}
}
