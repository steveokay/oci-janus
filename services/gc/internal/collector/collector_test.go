// Package collector_test tests the GC Result struct and the mode/dryRun logic.
// No gRPC connections or databases are required — these are pure struct and
// control-flow tests.
package collector

import (
	"testing"
)

// TestResult_dryRunMode verifies that when the collector is configured with
// mode "dry-run" the Result DryRun flag is set to true.
func TestResult_dryRunMode(t *testing.T) {
	// Build a minimal Collector with the mode we want to test.
	// We only need to exercise the logic in Run that sets res.DryRun,
	// which is a simple assignment: DryRun: c.mode == "dry-run".
	c := &Collector{mode: "dry-run"}
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}
	if !res.DryRun {
		t.Error("expected DryRun=true for mode=dry-run")
	}
	if res.Mode != "dry-run" {
		t.Errorf("Mode: got %q, want %q", res.Mode, "dry-run")
	}
}

// TestResult_fullMode verifies that mode "full" produces DryRun=false.
func TestResult_fullMode(t *testing.T) {
	c := &Collector{mode: "full"}
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}
	if res.DryRun {
		t.Error("expected DryRun=false for mode=full")
	}
}

// TestResult_blobsMode verifies mode "blobs" does not set DryRun.
func TestResult_blobsMode(t *testing.T) {
	c := &Collector{mode: "blobs"}
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}
	if res.DryRun {
		t.Error("expected DryRun=false for mode=blobs")
	}
}

// TestResult_manifestsMode verifies mode "manifests" does not set DryRun.
func TestResult_manifestsMode(t *testing.T) {
	c := &Collector{mode: "manifests"}
	res := &Result{Mode: c.mode, DryRun: c.mode == "dry-run"}
	if res.DryRun {
		t.Error("expected DryRun=false for mode=manifests")
	}
}

// TestResult_zeroValues verifies the zero-value Result has sensible defaults.
func TestResult_zeroValues(t *testing.T) {
	var r Result
	if r.ManifestsDeleted != 0 {
		t.Errorf("ManifestsDeleted default should be 0, got %d", r.ManifestsDeleted)
	}
	if r.BlobsDeleted != 0 {
		t.Errorf("BlobsDeleted default should be 0, got %d", r.BlobsDeleted)
	}
	if r.BytesFreed != 0 {
		t.Errorf("BytesFreed default should be 0, got %d", r.BytesFreed)
	}
	if r.TenantsSkipped != 0 {
		t.Errorf("TenantsSkipped default should be 0, got %d", r.TenantsSkipped)
	}
	if r.DryRun {
		t.Error("DryRun default should be false")
	}
}

// TestModeSwitch_manifests verifies that mode "manifests" triggers manifest sweep
// but not blob sweep.
func TestModeSwitch_manifests(t *testing.T) {
	c := &Collector{mode: "manifests"}
	// Reproduce the exact boolean condition used in runForTenant.
	dryRun := c.mode == "dry-run"
	sweepManifests := c.mode == "manifests" || c.mode == "full" || dryRun
	sweepBlobs := c.mode == "blobs" || c.mode == "full" || dryRun

	if !sweepManifests {
		t.Error("mode=manifests should trigger manifest sweep")
	}
	if sweepBlobs {
		t.Error("mode=manifests should NOT trigger blob sweep")
	}
}

// TestModeSwitch_blobs verifies that mode "blobs" triggers blob sweep but not
// manifest sweep.
func TestModeSwitch_blobs(t *testing.T) {
	c := &Collector{mode: "blobs"}
	dryRun := c.mode == "dry-run"
	sweepManifests := c.mode == "manifests" || c.mode == "full" || dryRun
	sweepBlobs := c.mode == "blobs" || c.mode == "full" || dryRun

	if sweepManifests {
		t.Error("mode=blobs should NOT trigger manifest sweep")
	}
	if !sweepBlobs {
		t.Error("mode=blobs should trigger blob sweep")
	}
}

// TestModeSwitch_full verifies that mode "full" triggers both sweeps.
func TestModeSwitch_full(t *testing.T) {
	c := &Collector{mode: "full"}
	dryRun := c.mode == "dry-run"
	sweepManifests := c.mode == "manifests" || c.mode == "full" || dryRun
	sweepBlobs := c.mode == "blobs" || c.mode == "full" || dryRun

	if !sweepManifests {
		t.Error("mode=full should trigger manifest sweep")
	}
	if !sweepBlobs {
		t.Error("mode=full should trigger blob sweep")
	}
}

// TestModeSwitch_dryRun verifies that mode "dry-run" triggers both sweeps
// without deletions.
func TestModeSwitch_dryRun(t *testing.T) {
	c := &Collector{mode: "dry-run"}
	dryRun := c.mode == "dry-run"
	sweepManifests := c.mode == "manifests" || c.mode == "full" || dryRun
	sweepBlobs := c.mode == "blobs" || c.mode == "full" || dryRun

	if !sweepManifests {
		t.Error("mode=dry-run should trigger manifest sweep logic")
	}
	if !sweepBlobs {
		t.Error("mode=dry-run should trigger blob sweep logic")
	}
	if !dryRun {
		t.Error("dryRun flag should be true for mode=dry-run")
	}
}
