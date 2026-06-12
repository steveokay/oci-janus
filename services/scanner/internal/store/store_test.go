// Package store_test exercises the in-memory scan state Store. All tests are
// fully self-contained — no gRPC, database, or network calls are needed.
package store

import (
	"testing"
	"time"
)

// TestStore_createAndGet verifies that a newly created scan record can be
// retrieved with its initial StatusPending state.
func TestStore_createAndGet(t *testing.T) {
	s := New()
	s.Create("scan-1", "tenant-a", "sha256:abc", "myorg/myimage")

	rec, ok := s.Get("scan-1")
	if !ok {
		t.Fatal("Get: expected record to exist, got false")
	}
	if rec.ScanID != "scan-1" {
		t.Errorf("ScanID: got %q, want %q", rec.ScanID, "scan-1")
	}
	if rec.TenantID != "tenant-a" {
		t.Errorf("TenantID: got %q, want %q", rec.TenantID, "tenant-a")
	}
	if rec.ManifestDigest != "sha256:abc" {
		t.Errorf("ManifestDigest: got %q, want %q", rec.ManifestDigest, "sha256:abc")
	}
	if rec.Status != StatusPending {
		t.Errorf("Status: got %q, want %q", rec.Status, StatusPending)
	}
	if rec.CompletedAt != nil {
		t.Error("CompletedAt should be nil for a pending scan")
	}
}

// TestStore_setRunning verifies the StatusRunning transition.
func TestStore_setRunning(t *testing.T) {
	s := New()
	s.Create("scan-2", "tenant-b", "sha256:def", "myorg/myrepo")
	s.SetRunning("scan-2")

	rec, ok := s.Get("scan-2")
	if !ok {
		t.Fatal("Get after SetRunning: not found")
	}
	if rec.Status != StatusRunning {
		t.Errorf("Status: got %q, want %q", rec.Status, StatusRunning)
	}
}

// TestStore_setComplete verifies the StatusComplete transition and that
// SeverityCounts and CompletedAt are set correctly.
func TestStore_setComplete(t *testing.T) {
	s := New()
	s.Create("scan-3", "tenant-c", "sha256:ghi", "org/repo")
	s.SetRunning("scan-3")

	before := time.Now()
	counts := map[string]int{"CRITICAL": 2, "HIGH": 5, "MEDIUM": 3}
	s.SetComplete("scan-3", counts)
	after := time.Now()

	rec, ok := s.Get("scan-3")
	if !ok {
		t.Fatal("Get after SetComplete: not found")
	}
	if rec.Status != StatusComplete {
		t.Errorf("Status: got %q, want %q", rec.Status, StatusComplete)
	}
	if rec.SeverityCounts["CRITICAL"] != 2 {
		t.Errorf("CRITICAL count: got %d, want 2", rec.SeverityCounts["CRITICAL"])
	}
	if rec.CompletedAt == nil {
		t.Fatal("CompletedAt should be set after completion")
	}
	if rec.CompletedAt.Before(before) || rec.CompletedAt.After(after) {
		t.Errorf("CompletedAt %v is outside expected window [%v, %v]",
			rec.CompletedAt, before, after)
	}
}

// TestStore_setFailed verifies the StatusFailed transition and that CompletedAt
// is set.
func TestStore_setFailed(t *testing.T) {
	s := New()
	s.Create("scan-4", "tenant-d", "sha256:jkl", "org/repo")
	s.SetFailed("scan-4")

	rec, ok := s.Get("scan-4")
	if !ok {
		t.Fatal("Get after SetFailed: not found")
	}
	if rec.Status != StatusFailed {
		t.Errorf("Status: got %q, want %q", rec.Status, StatusFailed)
	}
	if rec.CompletedAt == nil {
		t.Error("CompletedAt should be set after failure")
	}
}

// TestStore_getNotFound verifies that Get returns false for an unknown scan ID.
func TestStore_getNotFound(t *testing.T) {
	s := New()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for unknown scan ID")
	}
}

// TestStore_setRunning_noOp verifies that calling SetRunning on an unknown ID
// does not panic and leaves the store unmodified.
func TestStore_setRunning_noOp(t *testing.T) {
	s := New()
	// Should not panic.
	s.SetRunning("unknown-id")
	_, ok := s.Get("unknown-id")
	if ok {
		t.Error("SetRunning on unknown ID should not create a record")
	}
}

// TestStore_setComplete_noOp verifies that SetComplete on an unknown ID does
// not panic.
func TestStore_setComplete_noOp(t *testing.T) {
	s := New()
	s.SetComplete("unknown-id", map[string]int{"CRITICAL": 1})
	_, ok := s.Get("unknown-id")
	if ok {
		t.Error("SetComplete on unknown ID should not create a record")
	}
}

// TestStore_setFailed_noOp verifies that SetFailed on an unknown ID does not
// panic.
func TestStore_setFailed_noOp(t *testing.T) {
	s := New()
	s.SetFailed("unknown-id")
	_, ok := s.Get("unknown-id")
	if ok {
		t.Error("SetFailed on unknown ID should not create a record")
	}
}

// TestStore_getCopyIsolation verifies that modifying the returned ScanRecord
// does not mutate the stored original (Get returns a shallow copy).
func TestStore_getCopyIsolation(t *testing.T) {
	s := New()
	s.Create("scan-5", "tenant-e", "sha256:mno", "org/repo")

	rec, _ := s.Get("scan-5")
	rec.Status = "tampered"

	// The original must still have StatusPending.
	original, _ := s.Get("scan-5")
	if original.Status != StatusPending {
		t.Errorf("mutating returned copy changed stored record: got %q, want %q",
			original.Status, StatusPending)
	}
}

// TestStatusConstants_values verifies the string values of the status constants
// match the registry-metadata CHECK constraint in CLAUDE.md §4.5.
func TestStatusConstants_values(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "StatusPending", value: StatusPending, want: "pending"},
		{name: "StatusRunning", value: StatusRunning, want: "running"},
		{name: "StatusComplete", value: StatusComplete, want: "complete"},
		{name: "StatusFailed", value: StatusFailed, want: "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.value, tc.want)
			}
		})
	}
}
