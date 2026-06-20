// Package repository — unit tests for FE-API-014 and FE-API-015 helpers
// that don't require a live PostgreSQL connection.
//
// The cursor encoding and sort routines are pure functions, so they're
// covered here. The SQL paths are exercised via integration tests in
// repository_integration_test.go (testcontainers Postgres) — but those need
// the docker daemon and are skipped under the unit-test target.
package repository

import (
	"testing"
	"time"
)

// TestSeverityRank_canonicalSeveritiesSortAscending ensures the rank
// function preserves CRITICAL < HIGH < MEDIUM < LOW < NEGLIGIBLE so that
// ascending sort surfaces the most urgent CVEs first.
func TestSeverityRank_canonicalSeveritiesSortAscending(t *testing.T) {
	tests := []struct {
		in  string
		out int
	}{
		{"CRITICAL", 1}, {"HIGH", 2}, {"MEDIUM", 3}, {"LOW", 4},
		{"NEGLIGIBLE", 5}, {"high", 2}, {"", 99}, {"BOGUS", 99},
	}
	for _, tc := range tests {
		if got := severityRank(tc.in); got != tc.out {
			t.Errorf("severityRank(%q) = %d, want %d", tc.in, got, tc.out)
		}
	}
}

// TestSortVulnerabilityRows_orderedBySeverityThenCVE verifies the in-memory
// rollup sort produces a stable (severity_rank, cve_id) ascending order.
func TestSortVulnerabilityRows_orderedBySeverityThenCVE(t *testing.T) {
	rows := []VulnerabilityRow{
		{CVE: "CVE-3", Severity: "HIGH"},
		{CVE: "CVE-1", Severity: "CRITICAL"},
		{CVE: "CVE-2", Severity: "HIGH"},
		{CVE: "CVE-4", Severity: "LOW"},
	}
	sortVulnerabilityRows(rows)
	want := []string{"CVE-1", "CVE-2", "CVE-3", "CVE-4"}
	for i, w := range want {
		if rows[i].CVE != w {
			t.Errorf("position %d: got %s, want %s", i, rows[i].CVE, w)
		}
	}
}

// TestVulnerabilityCursor_roundTrip verifies encode/decode are inverses
// for valid input.
func TestVulnerabilityCursor_roundTrip(t *testing.T) {
	in := vulnerabilityCursor{SeverityRank: 2, CVEID: "CVE-2024-1234"}
	tok := encodeVulnerabilityCursor(in)
	out, err := decodeVulnerabilityCursor(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

// TestVulnerabilityCursor_emptyReturnsZero verifies an empty token decodes
// to the zero cursor (the "no filter" sentinel used by ListTenantVulnerabilities).
func TestVulnerabilityCursor_emptyReturnsZero(t *testing.T) {
	out, err := decodeVulnerabilityCursor("")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != (vulnerabilityCursor{}) {
		t.Errorf("expected zero cursor, got %+v", out)
	}
}

// TestVulnerabilityCursor_invalidBase64Returns error verifies a garbage
// token is rejected so the handler can surface InvalidArgument.
func TestVulnerabilityCursor_invalidBase64ReturnsError(t *testing.T) {
	if _, err := decodeVulnerabilityCursor("!!!not-base64!!!"); err == nil {
		t.Errorf("expected decode error, got nil")
	}
}

// TestScanHistoryCursor_roundTrip verifies the scan-history cursor encodes
// completed_at as RFC3339Nano with the scan_id preserved verbatim.
func TestScanHistoryCursor_roundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 20, 12, 30, 45, 123_000_000, time.UTC)
	in := scanHistoryCursor{CompletedAt: ts, ScanID: "uuid-1"}
	tok := encodeScanHistoryCursor(in)
	out, err := decodeScanHistoryCursor(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.CompletedAt.Equal(in.CompletedAt) || out.ScanID != in.ScanID {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

// TestScanHistoryCursor_malformedReturnsError verifies a non-pipe-delimited
// payload is rejected.
func TestScanHistoryCursor_malformedReturnsError(t *testing.T) {
	// base64 of "no-separator" — decodes cleanly but doesn't contain "|".
	if _, err := decodeScanHistoryCursor("bm8tc2VwYXJhdG9y"); err == nil {
		t.Errorf("expected decode error for missing separator, got nil")
	}
}
