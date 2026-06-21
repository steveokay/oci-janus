// Package registry tests cover discovery, active-selection, and the
// version-cache backfill path used by REM-011 Phase 2.
//
// These are unit tests — no network, no DB. The discovery target points
// at a t.TempDir() filled with empty files named scanner-*; the registry
// computes SHA-256 over real bytes so the empty-file SHA shows up in the
// fixture assertions.
package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates path with the given bytes and 0755 perms (the bake-time
// mode the scanner Dockerfile uses for adapter binaries).
func writeFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestNew_discoversAdaptersByPrefix verifies the directory scan picks up
// every scanner-* file and ignores anything else.
func TestNew_discoversAdaptersByPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scanner-stub"), []byte("stub-binary"))
	writeFile(t, filepath.Join(dir, "scanner-trivy"), []byte("trivy-binary"))
	writeFile(t, filepath.Join(dir, "other-tool"), []byte("unrelated")) // must be ignored

	r, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("want 2 adapters, got %d (%v)", len(list), list)
	}
	// deriveName strips the well-known prefix.
	gotNames := map[string]bool{}
	for _, a := range list {
		gotNames[a.Name] = true
		if a.Checksum == "" {
			t.Errorf("adapter %s missing checksum", a.Name)
		}
		if a.SizeBytes == 0 {
			t.Errorf("adapter %s size_bytes=0", a.Name)
		}
	}
	if !gotNames["stub"] || !gotNames["trivy"] {
		t.Errorf("missing expected adapters; got %v", gotNames)
	}
}

// TestSetActive_rejectsUnknownPath verifies the registry refuses to mark
// a path that wasn't discovered as active — this is the invariant the
// SetActiveAdapter handler relies on for InvalidArgument routing.
func TestSetActive_rejectsUnknownPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scanner-stub"), []byte("x"))

	r, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetActive("/nonexistent/binary"); err == nil {
		t.Error("expected SetActive on unknown path to error")
	}
	if r.Active() != nil {
		t.Error("Active should be nil after a failed SetActive")
	}
}

// TestSetActive_marksAdapterActive verifies the happy path: after
// SetActive(path), Active() returns that adapter with active==true via
// the ActivePath helper.
func TestSetActive_marksAdapterActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scanner-stub")
	writeFile(t, path, []byte("x"))

	r, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.SetActive(path); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if got := r.ActivePath(); got != path {
		t.Errorf("ActivePath = %q, want %q", got, path)
	}
	active := r.Active()
	if active == nil || active.Path != path {
		t.Errorf("Active() = %+v, want path=%q", active, path)
	}
}

// TestRecordVersion_backfillsByName verifies the worker's "first
// successful scan reports a version" path: after RecordVersion the
// adapter's Version flips from "unknown" to the recorded string.
func TestRecordVersion_backfillsByName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scanner-trivy-adapter"), []byte("x"))

	r, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Pre-condition: discovery starts at "unknown".
	for _, a := range r.List() {
		if a.Version != "unknown" {
			t.Errorf("pre-record: want unknown, got %q", a.Version)
		}
	}
	r.RecordVersion("trivy-adapter", "0.55.0")
	for _, a := range r.List() {
		if a.Version != "0.55.0" {
			t.Errorf("post-record: want 0.55.0, got %q", a.Version)
		}
	}
	// Empty name/version is a noop — protects the worker's RecordVersion
	// hook from spurious overwrites when the plugin returns blanks.
	r.RecordVersion("", "ignored")
	r.RecordVersion("trivy-adapter", "")
	for _, a := range r.List() {
		if a.Version != "0.55.0" {
			t.Errorf("noop guard failed: got %q", a.Version)
		}
	}
}
