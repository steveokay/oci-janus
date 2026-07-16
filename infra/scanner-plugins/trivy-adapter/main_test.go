package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// REM-019 Phase 2: the scan hot path used to run `trivy rootfs` without
// --skip-db-update, so every scan against a stale/absent DB paid a live
// ~100MB download that made scans slow + intermittently fail ("failed"
// scan_results with scanner_name="unknown"). The fix runs offline
// (--skip-db-update) whenever the DB is already present, falling back to a
// one-time online fetch only for a genuinely cold cache. These tests pin the
// two pure helpers that decision rests on.

func TestTrivyScanArgs_skipsDBUpdateWhenPresent(t *testing.T) {
	args := trivyScanArgs("/tmp/rootfs", true)
	if !slices.Contains(args, "--skip-db-update") {
		t.Errorf("expected --skip-db-update in offline args, got %v", args)
	}
	if args[0] != "rootfs" {
		t.Errorf("first arg must be the trivy subcommand rootfs, got %v", args)
	}
	if args[len(args)-1] != "/tmp/rootfs" {
		t.Errorf("last arg must be the rootfs path, got %v", args)
	}
	// The report-shaping flags must be preserved.
	for _, want := range []string{"--quiet", "--no-progress", "--format", "json"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in args %v", want, args)
		}
	}
}

func TestTrivyScanArgs_onlineWhenAbsent(t *testing.T) {
	args := trivyScanArgs("/tmp/rootfs", false)
	if slices.Contains(args, "--skip-db-update") {
		t.Errorf("cold-cache run must allow a DB download (no --skip-db-update), got %v", args)
	}
}

func TestTrivyDBPresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRIVY_CACHE_DIR", dir)

	if trivyDBPresent() {
		t.Fatal("DB must be reported absent for an empty cache dir")
	}

	dbDir := filepath.Join(dir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "trivy.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !trivyDBPresent() {
		t.Fatal("DB must be reported present once db/trivy.db exists")
	}
}
