// Package repository — unit tests for FE-API-017 helpers that don't require
// a live PostgreSQL connection. Cursor encoding, in-memory rollup, sort, and
// cursor skip are all pure functions, so they're covered here against
// synthetic scan rows.
package repository

import (
	"testing"
)

// ── cursor round-trip ───────────────────────────────────────────────────────

// TestRemediationCursor_roundTrip verifies encode/decode are inverses for
// well-formed input across the full ordering tuple.
func TestRemediationCursor_roundTrip(t *testing.T) {
	in := remediationCursor{
		SeverityRank: 2,
		NegCVECount:  -7,
		PackageName:  "openssl",
		FromVersion:  "1.0.0",
		ToVersion:    "1.0.1",
	}
	tok := encodeRemediationCursor(in)
	out, err := decodeRemediationCursor(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

// TestRemediationCursor_emptyReturnsZero verifies an empty token decodes to
// the zero cursor (first-page sentinel).
func TestRemediationCursor_emptyReturnsZero(t *testing.T) {
	out, err := decodeRemediationCursor("")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != (remediationCursor{}) {
		t.Errorf("expected zero cursor, got %+v", out)
	}
}

// TestRemediationCursor_invalidBase64ReturnsError ensures garbage input is
// rejected so the handler can surface InvalidArgument → 400.
func TestRemediationCursor_invalidBase64ReturnsError(t *testing.T) {
	if _, err := decodeRemediationCursor("!!!not-base64!!!"); err == nil {
		t.Errorf("expected decode error, got nil")
	}
}

// TestRemediationCursor_missingSeparatorReturnsError exercises the
// "5 fields required" guard. A base64-clean but non-pipe-delimited payload
// must surface as an error.
func TestRemediationCursor_missingSeparatorReturnsError(t *testing.T) {
	// base64 of "no|separators" — only two pipes, parser expects four.
	if _, err := decodeRemediationCursor("bm98c2VwYXJhdG9ycw=="); err == nil {
		t.Errorf("expected decode error for missing separators, got nil")
	}
}

// ── sort order ──────────────────────────────────────────────────────────────

// TestSortRemediationRows_orderedBySeverityThenCountThenPackage verifies the
// in-memory sort produces the documented ordering tuple:
//
//	(max_severity_rank ASC, cves_fixed_count DESC, package_name ASC,
//	 from_version ASC, to_version ASC).
func TestSortRemediationRows_orderedBySeverityThenCountThenPackage(t *testing.T) {
	rows := []RemediationRow{
		// 2 fixed, MEDIUM
		{PackageName: "zlib", MaxSeverity: "MEDIUM", CVEsFixedCount: 2, FromVersion: "1.0", ToVersion: "1.1"},
		// 5 fixed, HIGH — should beat openssl on count tie? no, different sev
		{PackageName: "curl", MaxSeverity: "HIGH", CVEsFixedCount: 5, FromVersion: "7.0", ToVersion: "7.1"},
		// 7 fixed, CRITICAL — most urgent
		{PackageName: "openssl", MaxSeverity: "CRITICAL", CVEsFixedCount: 7, FromVersion: "1.0.0", ToVersion: "1.0.1"},
		// 3 fixed, HIGH — tie on severity with curl; curl has higher count
		{PackageName: "bash", MaxSeverity: "HIGH", CVEsFixedCount: 3, FromVersion: "4.4", ToVersion: "4.5"},
		// 5 fixed, HIGH — tie on (sev, count) with curl; alpha-sort by package: curl < zsh
		{PackageName: "zsh", MaxSeverity: "HIGH", CVEsFixedCount: 5, FromVersion: "5.0", ToVersion: "5.1"},
	}
	sortRemediationRows(rows)
	want := []string{"openssl", "curl", "zsh", "bash", "zlib"}
	for i, w := range want {
		if rows[i].PackageName != w {
			t.Errorf("position %d: got %s, want %s", i, rows[i].PackageName, w)
		}
	}
}

// TestSortRemediationRows_tieBreakOnVersions verifies that when (severity,
// count, package) all match, sort falls through to from_version then
// to_version. Mirrors the cursor's five-tuple ordering.
func TestSortRemediationRows_tieBreakOnVersions(t *testing.T) {
	rows := []RemediationRow{
		{PackageName: "x", MaxSeverity: "HIGH", CVEsFixedCount: 1, FromVersion: "1.0", ToVersion: "1.2"},
		{PackageName: "x", MaxSeverity: "HIGH", CVEsFixedCount: 1, FromVersion: "1.0", ToVersion: "1.1"},
		{PackageName: "x", MaxSeverity: "HIGH", CVEsFixedCount: 1, FromVersion: "0.9", ToVersion: "1.0"},
	}
	sortRemediationRows(rows)
	// (0.9 → 1.0) sorts before (1.0 → 1.1) sorts before (1.0 → 1.2).
	if rows[0].FromVersion != "0.9" || rows[1].ToVersion != "1.1" || rows[2].ToVersion != "1.2" {
		t.Errorf("unexpected version tie-break: %+v", rows)
	}
}

// ── cursorAfter ─────────────────────────────────────────────────────────────

// TestCursorAfter_strictlyAfterPrecedingFields exercises each of the five
// cursor fields in turn: changing any single field in the "after" direction
// flips the predicate, and equality on the full tuple is NOT after.
func TestCursorAfter_strictlyAfterPrecedingFields(t *testing.T) {
	base := remediationCursor{
		SeverityRank: 2, NegCVECount: -5,
		PackageName: "openssl", FromVersion: "1.0", ToVersion: "1.1",
	}
	row := RemediationRow{
		MaxSeverity: "HIGH", CVEsFixedCount: 5,
		PackageName: "openssl", FromVersion: "1.0", ToVersion: "1.1",
	}
	// Identical row is NOT strictly after.
	if cursorAfter(row, base) {
		t.Errorf("identical row should not be after cursor")
	}
	// Lower severity (higher rank number = MEDIUM after HIGH).
	row2 := row
	row2.MaxSeverity = "MEDIUM"
	if !cursorAfter(row2, base) {
		t.Errorf("MEDIUM should be after HIGH cursor")
	}
	// Same severity, fewer CVEs fixed.
	row3 := row
	row3.CVEsFixedCount = 3
	if !cursorAfter(row3, base) {
		t.Errorf("fewer CVEs should be after when severity matches")
	}
	// Same severity + count, alphabetically later package.
	row4 := row
	row4.PackageName = "zlib"
	if !cursorAfter(row4, base) {
		t.Errorf("alphabetically later package should be after cursor")
	}
}

// ── rollup ──────────────────────────────────────────────────────────────────

// TestRollupRemediations_noFindings handles the empty-findings input cleanly.
func TestRollupRemediations_noFindings(t *testing.T) {
	got := rollupRemediations(nil)
	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
}

// TestRollupRemediations_skipsFindingsWithoutFixedIn verifies findings that
// can't be remediated (no FixedIn, or FixedIn == installed version) are
// dropped from the grouping.
func TestRollupRemediations_skipsFindingsWithoutFixedIn(t *testing.T) {
	scanRows := []remediationScanRow{
		{Repo: "acme/api", Tag: "v1", Digest: "sha256:abc", Findings: []scannerFinding{
			// Drop: no FixedIn.
			{CVE: "CVE-1", Severity: "HIGH", Package: "p", Version: "1.0"},
			// Drop: FixedIn == installed version.
			{CVE: "CVE-2", Severity: "HIGH", Package: "p", Version: "1.0", FixedIn: "1.0"},
			// Keep.
			{CVE: "CVE-3", Severity: "HIGH", Package: "p", Version: "1.0", FixedIn: "1.1"},
		}},
	}
	got := rollupRemediations(scanRows)
	if len(got) != 1 {
		t.Fatalf("expected 1 actionable group, got %d", len(got))
	}
	if got[0].CVEsFixedCount != 1 || got[0].CVEsFixed[0] != "CVE-3" {
		t.Errorf("unexpected group: %+v", got[0])
	}
}

// TestRollupRemediations_multiSeverityKeepsMax verifies that when the same
// (package, from, to) grouping has findings at multiple severities, the
// rolled-up MaxSeverity is the most urgent.
func TestRollupRemediations_multiSeverityKeepsMax(t *testing.T) {
	scanRows := []remediationScanRow{
		{Repo: "acme/api", Tag: "v1", Digest: "sha256:abc", Findings: []scannerFinding{
			{CVE: "CVE-low", Severity: "LOW", Package: "openssl", Version: "1.0.0", FixedIn: "1.0.1"},
			{CVE: "CVE-crit", Severity: "CRITICAL", Package: "openssl", Version: "1.0.0", FixedIn: "1.0.1"},
			{CVE: "CVE-med", Severity: "MEDIUM", Package: "openssl", Version: "1.0.0", FixedIn: "1.0.1"},
		}},
	}
	got := rollupRemediations(scanRows)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].MaxSeverity != "CRITICAL" {
		t.Errorf("MaxSeverity: got %s, want CRITICAL", got[0].MaxSeverity)
	}
	if got[0].CVEsFixedCount != 3 {
		t.Errorf("CVEsFixedCount: got %d, want 3", got[0].CVEsFixedCount)
	}
}

// TestRollupRemediations_multiRepoAffectedListsDeduped exercises the
// affected-tuple dedup across multiple scan rows producing the same CVE.
// All four distinct (repo, tag) tuples should land in Affected; identical
// tuples are deduplicated.
func TestRollupRemediations_multiRepoAffectedListsDeduped(t *testing.T) {
	mkRow := func(repo, tag, digest string) remediationScanRow {
		return remediationScanRow{Repo: repo, Tag: tag, Digest: digest, Findings: []scannerFinding{
			{CVE: "CVE-1", Severity: "HIGH", Package: "p", Version: "1", FixedIn: "2"},
		}}
	}
	scanRows := []remediationScanRow{
		mkRow("acme/api", "v1", "sha256:1"),
		mkRow("acme/api", "v1", "sha256:1"), // duplicate — dropped
		mkRow("acme/api", "v2", "sha256:2"),
		mkRow("acme/web", "v1", "sha256:3"),
		mkRow("acme/db", "v1", "sha256:4"),
	}
	got := rollupRemediations(scanRows)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].AffectedCount != 4 {
		t.Errorf("AffectedCount: got %d, want 4", got[0].AffectedCount)
	}
	if len(got[0].Affected) != 4 {
		t.Errorf("Affected len: got %d, want 4", len(got[0].Affected))
	}
}

// TestRollupRemediations_capsAffectedAtTen verifies the affectedCap is
// applied to the Affected slice while AffectedCount still reports the true
// total — the contract the dashboard relies on for "N affected (showing 10)"
// rendering.
func TestRollupRemediations_capsAffectedAtTen(t *testing.T) {
	scanRows := make([]remediationScanRow, 0, 15)
	for i := 0; i < 15; i++ {
		scanRows = append(scanRows, remediationScanRow{
			Repo:   "acme/svc",
			Tag:    "v" + string(rune('a'+i)), // distinct tag per row → distinct tuple
			Digest: "sha256:" + string(rune('a'+i)),
			Findings: []scannerFinding{
				{CVE: "CVE-shared", Severity: "HIGH", Package: "p", Version: "1", FixedIn: "2"},
			},
		})
	}
	got := rollupRemediations(scanRows)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].AffectedCount != 15 {
		t.Errorf("AffectedCount: got %d, want 15", got[0].AffectedCount)
	}
	if len(got[0].Affected) != affectedCap {
		t.Errorf("Affected len: got %d, want %d (cap)", len(got[0].Affected), affectedCap)
	}
}

// TestRollupRemediations_orderingPlacesHigherSeverityFirst exercises the
// end-to-end happy path: rollup + sort surfaces the CRITICAL group ahead of
// HIGH despite HIGH having more CVEs.
func TestRollupRemediations_orderingPlacesHigherSeverityFirst(t *testing.T) {
	scanRows := []remediationScanRow{
		{Repo: "acme/a", Tag: "v1", Digest: "sha256:1", Findings: []scannerFinding{
			// One CRITICAL group for openssl 1.0.0 → 1.0.1.
			{CVE: "CVE-c1", Severity: "CRITICAL", Package: "openssl", Version: "1.0.0", FixedIn: "1.0.1"},
		}},
		{Repo: "acme/a", Tag: "v2", Digest: "sha256:2", Findings: []scannerFinding{
			// Three HIGH findings for zlib 1.0 → 1.1 — higher count but
			// lower severity than CRITICAL group above.
			{CVE: "CVE-h1", Severity: "HIGH", Package: "zlib", Version: "1.0", FixedIn: "1.1"},
			{CVE: "CVE-h2", Severity: "HIGH", Package: "zlib", Version: "1.0", FixedIn: "1.1"},
			{CVE: "CVE-h3", Severity: "HIGH", Package: "zlib", Version: "1.0", FixedIn: "1.1"},
		}},
	}
	rows := rollupRemediations(scanRows)
	sortRemediationRows(rows)
	if len(rows) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(rows))
	}
	if rows[0].PackageName != "openssl" {
		t.Errorf("position 0: got %s, want openssl (CRITICAL beats HIGH)", rows[0].PackageName)
	}
	if rows[1].PackageName != "zlib" {
		t.Errorf("position 1: got %s, want zlib", rows[1].PackageName)
	}
}
