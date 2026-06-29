//go:build integration

// Package integration — REM-011 Phase 2 scanner_settings persistence
// round-trip. Verifies the singleton-row invariant + the upsert path
// against a real PostgreSQL container.
package integration

import (
	"context"
	"testing"
)

// TestScannerSettings_GetWhenMissing_returnsEmptyString verifies the
// boot-time fallback path: a fresh DB has no scanner_settings row, and
// GetActiveAdapter returns "" (not an error) so the caller falls back
// to the SCANNER_PLUGIN_PATH env var without log noise.
func TestScannerSettings_GetWhenMissing_returnsEmptyString(t *testing.T) {
	repo := newRepo(t)
	got, err := repo.GetActiveAdapter(context.Background())
	if err != nil {
		t.Fatalf("GetActiveAdapter on empty table: unexpected error %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// TestScannerSettings_UpsertThenGet_roundtrips verifies SetActiveAdapter
// inserts when absent + replaces on conflict (the single-row invariant).
func TestScannerSettings_UpsertThenGet_roundtrips(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	// First write — INSERT path.
	if err := repo.SetActiveAdapter(ctx, "/usr/local/bin/scanner-dev-stub", "system"); err != nil {
		t.Fatalf("initial SetActiveAdapter: %v", err)
	}
	got, err := repo.GetActiveAdapter(ctx)
	if err != nil {
		t.Fatalf("GetActiveAdapter: %v", err)
	}
	if got != "/usr/local/bin/scanner-dev-stub" {
		t.Errorf("after insert: got %q", got)
	}

	// Second write — UPSERT path. Different actor, different path.
	if err := repo.SetActiveAdapter(ctx, "/usr/local/bin/scanner-trivy-adapter", "user-123"); err != nil {
		t.Fatalf("second SetActiveAdapter: %v", err)
	}
	got, err = repo.GetActiveAdapter(ctx)
	if err != nil {
		t.Fatalf("GetActiveAdapter after upsert: %v", err)
	}
	if got != "/usr/local/bin/scanner-trivy-adapter" {
		t.Errorf("after upsert: got %q", got)
	}
}

// TestScannerSettings_EmptyPathRejected verifies the safety net for
// callers that accidentally pass an empty path — the repository fails
// fast with a clear error rather than letting the NOT NULL constraint
// surface as a wrapped pgx error.
func TestScannerSettings_EmptyPathRejected(t *testing.T) {
	repo := newRepo(t)
	if err := repo.SetActiveAdapter(context.Background(), "", "system"); err == nil {
		t.Error("expected empty path to error")
	}
}
