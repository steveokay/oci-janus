//go:build integration

// Package integration — vulnerability-count dedup regression coverage
// for S-MAINT-1 B1.
//
// The bug being guarded against: GetTenantVulnerabilityCount used to sum
// across every completed scan_results row, so re-scanning the same manifest
// doubled the dashboard severity totals. The fix (2026-06-22) wraps the
// SUM in a DISTINCT ON (repo_id, manifest_digest) CTE so only the latest
// scan per manifest contributes. These tests pin that behaviour.
package integration

import (
	"context"
	"testing"
)

// TestGetTenantVulnerabilityCount_dedupsRepeatScans seeds three scans of the
// same manifest with deliberately different severity counts and asserts that
// only the LATEST scan's counts contribute to the tenant total.
//
// Pre-fix (S-MAINT-1 B1): the SUM would have added all three (5+3+7 = 15 CRITICAL).
// Post-fix: only the third scan's counts survive (7 CRITICAL).
func TestGetTenantVulnerabilityCount_dedupsRepeatScans(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	// Seed: one org / one repo / one manifest digest.
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "vuln-dedup-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "vuln-dedup-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// Three scans of the same (repo_id, manifest_digest). UpsertScanResult
	// inserts a new row each time because the conflict key is scan_id and
	// every job gets a fresh UUID — this is by design (scan history), but
	// the aggregator must collapse them to the latest.
	scans := []struct {
		id       string
		critical int32
		high     int32
		medium   int32
	}{
		// Order matters for the dedup tie-break (completed_at DESC). The
		// third entry is the "latest" — its counts are the ones that
		// should appear in the totals.
		{"11111111-1111-1111-1111-111111111111", 5, 10, 20},
		{"22222222-2222-2222-2222-222222222222", 3, 8, 15},
		{"33333333-3333-3333-3333-333333333333", 7, 12, 25},
	}

	for _, s := range scans {
		if _, err := repo.CreatePendingScanResult(ctx, devTenantID, r.GetRepoId(), digest, "trivy", "0.50.0"); err != nil {
			t.Fatalf("CreatePendingScanResult: %v", err)
		}
		// UpsertScanResult writes the severity counts + flips status to
		// complete. The pending row we just created uses a fresh UUID,
		// so we pass the table-generated id back via a custom path:
		// the public API doesn't expose the row id, so we use the
		// dedicated insert path with a known id.
		if err := repo.UpsertScanResult(
			ctx,
			s.id, // scan_id (the ON CONFLICT key)
			devTenantID,
			"complete",
			[]byte(`[]`), // findings (unused by the count aggregate)
			map[string]int32{
				"CRITICAL": s.critical,
				"HIGH":     s.high,
				"MEDIUM":   s.medium,
			},
			r.GetRepoId(),
			digest,
			"trivy",
			"0.50.0",
		); err != nil {
			t.Fatalf("UpsertScanResult(%s): %v", s.id, err)
		}
	}

	total, critical, high, medium, low, negligible, err := repo.GetTenantVulnerabilityCount(ctx, devTenantID)
	if err != nil {
		t.Fatalf("GetTenantVulnerabilityCount: %v", err)
	}

	// Latest scan's counts: 7 CRITICAL + 12 HIGH + 25 MEDIUM = 44 total.
	wantCritical := int64(7)
	wantHigh := int64(12)
	wantMedium := int64(25)
	wantTotal := wantCritical + wantHigh + wantMedium // low + negligible default to 0

	if critical != wantCritical {
		t.Errorf("critical: got %d, want %d (pre-fix bug would have summed all 3 scans → 15)", critical, wantCritical)
	}
	if high != wantHigh {
		t.Errorf("high: got %d, want %d (pre-fix bug would have summed all 3 scans → 30)", high, wantHigh)
	}
	if medium != wantMedium {
		t.Errorf("medium: got %d, want %d (pre-fix bug would have summed all 3 scans → 60)", medium, wantMedium)
	}
	if low != 0 {
		t.Errorf("low: got %d, want 0", low)
	}
	if negligible != 0 {
		t.Errorf("negligible: got %d, want 0", negligible)
	}
	if total != wantTotal {
		t.Errorf("total: got %d, want %d", total, wantTotal)
	}
}

// TestGetTenantVulnerabilityCount_independentManifestsBothCount verifies the
// dedup applies per (repo_id, manifest_digest) — two DIFFERENT manifests in
// the same repo should both contribute. (Sanity check the fix didn't over-
// dedup.)
func TestGetTenantVulnerabilityCount_independentManifestsBothCount(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "vuln-multi-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "vuln-multi-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	manifests := []struct {
		digest   string
		scanID   string
		critical int32
	}{
		{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", 4},
		{"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "dddddddd-dddd-dddd-dddd-dddddddddddd", 6},
	}

	for _, m := range manifests {
		if _, err := repo.CreatePendingScanResult(ctx, devTenantID, r.GetRepoId(), m.digest, "trivy", "0.50.0"); err != nil {
			t.Fatalf("CreatePendingScanResult: %v", err)
		}
		if err := repo.UpsertScanResult(
			ctx,
			m.scanID,
			devTenantID,
			"complete",
			[]byte(`[]`),
			map[string]int32{"CRITICAL": m.critical},
			r.GetRepoId(),
			m.digest,
			"trivy",
			"0.50.0",
		); err != nil {
			t.Fatalf("UpsertScanResult: %v", err)
		}
	}

	_, critical, _, _, _, _, err := repo.GetTenantVulnerabilityCount(ctx, devTenantID)
	if err != nil {
		t.Fatalf("GetTenantVulnerabilityCount: %v", err)
	}

	want := int64(4 + 6) // both manifests count (different digests)
	if critical != want {
		t.Errorf("critical: got %d, want %d (two independent manifests should both contribute)", critical, want)
	}
}

// TestGetTenantVulnerabilityCount_failedScansExcluded verifies the dedup
// query keeps the existing status='complete' filter intact. A failed scan
// must not displace the latest complete scan in the per-manifest pick.
func TestGetTenantVulnerabilityCount_failedScansExcluded(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "vuln-status-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "vuln-status-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	digest := "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	// First scan: complete with real findings.
	if _, err := repo.CreatePendingScanResult(ctx, devTenantID, r.GetRepoId(), digest, "trivy", "0.50.0"); err != nil {
		t.Fatalf("CreatePendingScanResult #1: %v", err)
	}
	if err := repo.UpsertScanResult(
		ctx,
		"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		devTenantID,
		"complete",
		[]byte(`[]`),
		map[string]int32{"CRITICAL": 9},
		r.GetRepoId(),
		digest,
		"trivy",
		"0.50.0",
	); err != nil {
		t.Fatalf("UpsertScanResult complete: %v", err)
	}

	// Second scan attempt: failed. Must not zero out the previous total.
	if _, err := repo.CreatePendingScanResult(ctx, devTenantID, r.GetRepoId(), digest, "trivy", "0.50.0"); err != nil {
		t.Fatalf("CreatePendingScanResult #2: %v", err)
	}
	if err := repo.UpsertScanResult(
		ctx,
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		devTenantID,
		"failed",
		[]byte(`[]`),
		map[string]int32{}, // failed scans report no findings
		r.GetRepoId(),
		digest,
		"trivy",
		"0.50.0",
	); err != nil {
		t.Fatalf("UpsertScanResult failed: %v", err)
	}

	_, critical, _, _, _, _, err := repo.GetTenantVulnerabilityCount(ctx, devTenantID)
	if err != nil {
		t.Fatalf("GetTenantVulnerabilityCount: %v", err)
	}

	want := int64(9) // last *complete* scan's count survives; the failed scan is ignored
	if critical != want {
		t.Errorf("critical: got %d, want %d (failed scans must not displace the prior complete scan)", critical, want)
	}
}
