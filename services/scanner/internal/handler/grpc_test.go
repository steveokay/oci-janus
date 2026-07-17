// Package handler_test tests the GRPCHandler for the scanner service.
// All dependencies are satisfied by hand-written fakes — no gRPC server,
// no RabbitMQ, no database required.
package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	scannerregistry "github.com/steveokay/oci-janus/services/scanner/internal/registry"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"
)

// fakePool is a hand-written fake for *worker.Pool that records the arguments
// passed to TriggerScanJob and returns a predictable scan ID.
type fakePool struct {
	lastTenantID   string
	lastRepoID     string
	lastRepoName   string
	lastDigest     string
	returnedScanID string
}

// TriggerScanJob captures arguments and returns a pre-configured scan ID.
func (f *fakePool) TriggerScanJob(tenantID, repoID, repoName, manifestDigest string) string {
	f.lastTenantID = tenantID
	f.lastRepoID = repoID
	f.lastRepoName = repoName
	f.lastDigest = manifestDigest
	return f.returnedScanID
}

// handlerWithFakePool creates a GRPCHandler that wraps a fakePool.
// Because GRPCHandler holds a *worker.Pool (concrete type), we need to wire in
// a real Store and use the internal field directly via a helper constructor.
// The simplest approach: create a real Pool-shaped value and swap the internal
// store — but since New() takes concrete *worker.Pool, we test the handler
// through its public API with a real store and a thin wrapper pool.
//
// For methods that only touch scanStore (GetScanStatus), we don't need a pool
// at all — we pass nil. For TriggerScan, we need a pool that records the call.
//
// To avoid touching production source, we embed the pool indirectly through the
// existing New() constructor and a pre-populated store.

// TestGRPCHandler_GetScanStatus_notFound verifies that a missing scan_id
// returns codes.NotFound.
func TestGRPCHandler_GetScanStatus_notFound(t *testing.T) {
	sc := store.New()
	// Pass nil pool — GetScanStatus does not use the pool.
	h := New(nil, sc)

	_, err := h.GetScanStatus(context.Background(), &scannerv1.GetScanStatusRequest{
		ScanId: "does-not-exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown scan ID, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code: got %v, want %v", st.Code(), codes.NotFound)
	}
}

// TestGRPCHandler_GetScanStatus_emptyID verifies that an empty scan_id
// returns codes.InvalidArgument.
func TestGRPCHandler_GetScanStatus_emptyID(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.GetScanStatus(context.Background(), &scannerv1.GetScanStatusRequest{ScanId: ""})
	if err == nil {
		t.Fatal("expected error for empty scan_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestGRPCHandler_GetScanStatus_pending verifies that a pending scan is returned
// with the correct status string and no completed_at timestamp.
func TestGRPCHandler_GetScanStatus_pending(t *testing.T) {
	sc := store.New()
	sc.Create("scan-abc", "tenant-1", "sha256:deadbeef", "org/repo")

	h := New(nil, sc)
	resp, err := h.GetScanStatus(context.Background(), &scannerv1.GetScanStatusRequest{ScanId: "scan-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != store.StatusPending {
		t.Errorf("Status: got %q, want %q", resp.Status, store.StatusPending)
	}
	if resp.CompletedAt != nil {
		t.Error("CompletedAt should be nil for a pending scan")
	}
}

// TestGRPCHandler_GetScanStatus_complete verifies that a complete scan is returned
// with correct severity counts and a non-nil completed_at timestamp.
func TestGRPCHandler_GetScanStatus_complete(t *testing.T) {
	sc := store.New()
	sc.Create("scan-complete", "tenant-2", "sha256:abc123", "org/myrepo")
	sc.SetRunning("scan-complete")
	sc.SetComplete("scan-complete", map[string]int{"CRITICAL": 3, "HIGH": 1})

	h := New(nil, sc)
	resp, err := h.GetScanStatus(context.Background(), &scannerv1.GetScanStatusRequest{ScanId: "scan-complete"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != store.StatusComplete {
		t.Errorf("Status: got %q, want %q", resp.Status, store.StatusComplete)
	}
	if resp.SeverityCounts["CRITICAL"] != 3 {
		t.Errorf("CRITICAL: got %d, want 3", resp.SeverityCounts["CRITICAL"])
	}
	if resp.SeverityCounts["HIGH"] != 1 {
		t.Errorf("HIGH: got %d, want 1", resp.SeverityCounts["HIGH"])
	}
	if resp.CompletedAt == nil {
		t.Error("CompletedAt should be set for a complete scan")
	}
}

// TestGRPCHandler_GetScanStatus_failed verifies that a failed scan is returned
// with StatusFailed and a completed_at timestamp.
func TestGRPCHandler_GetScanStatus_failed(t *testing.T) {
	sc := store.New()
	sc.Create("scan-fail", "tenant-3", "sha256:fail", "org/repo")
	sc.SetFailed("scan-fail")

	h := New(nil, sc)
	resp, err := h.GetScanStatus(context.Background(), &scannerv1.GetScanStatusRequest{ScanId: "scan-fail"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != store.StatusFailed {
		t.Errorf("Status: got %q, want %q", resp.Status, store.StatusFailed)
	}
	if resp.CompletedAt == nil {
		t.Error("CompletedAt should be set after failure")
	}
}

// TestGRPCHandler_TriggerScan_missingFields verifies that an incomplete request
// (missing tenant_id) returns codes.InvalidArgument.
func TestGRPCHandler_TriggerScan_missingFields(t *testing.T) {
	sc := store.New()
	// For TriggerScan we need a real *worker.Pool. Since the pool itself will not
	// be invoked (validation fires first), we can pass nil safely — the handler
	// checks required fields before calling pool.TriggerScanJob.
	h := New(nil, sc)

	_, err := h.TriggerScan(context.Background(), &scannerv1.TriggerScanRequest{
		TenantId:       "",
		RepositoryName: "org/repo",
		ManifestDigest: "sha256:abc",
	})
	if err == nil {
		t.Fatal("expected error when tenant_id is empty")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestGRPCHandler_TriggerScan_missingDigest verifies that a missing manifest_digest
// returns codes.InvalidArgument.
func TestGRPCHandler_TriggerScan_missingDigest(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.TriggerScan(context.Background(), &scannerv1.TriggerScanRequest{
		TenantId:       "tenant-xyz",
		RepositoryName: "org/repo",
		ManifestDigest: "",
	})
	if err == nil {
		t.Fatal("expected error when manifest_digest is empty")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestTimestampProto_nilInput verifies that the internal timestampProto helper
// returns nil when passed a nil *time.Time (no panic).
func TestTimestampProto_nilInput(t *testing.T) {
	result := timestampProto(nil)
	if result != nil {
		t.Error("expected nil for nil input")
	}
}

// TestTimestampProto_nonNilInput verifies that a valid time is converted correctly.
func TestTimestampProto_nonNilInput(t *testing.T) {
	ts := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	result := timestampProto(&ts)
	if result == nil {
		t.Fatal("expected non-nil Timestamp")
	}
	if !result.AsTime().Equal(ts) {
		t.Errorf("time mismatch: got %v, want %v", result.AsTime(), ts)
	}
}

// poolAdapter wraps *worker.Pool so we can satisfy the handler constructor.
// This test uses a real pool with a non-nil scanStore so TriggerScanJob is callable
// but the internal gRPC clients are nil (validation prevents reaching them).
// We verify that a valid TriggerScan request returns a non-empty scan_id from the store.
func TestGRPCHandler_TriggerScan_validRequest_returnsScanID(t *testing.T) {
	sc := store.New()
	// Build a minimal Pool with nil gRPC conns and nil publisher.
	// TriggerScanJob calls sc.Create() and Enqueue() — Enqueue fires a goroutine
	// that calls runJob, which will panic on nil metaClient. We avoid that by
	// giving the pool a large enough buffer so the goroutine doesn't fire inline,
	// and we don't wait for the goroutine (it will fail quietly in background).
	// The important assertion is that a scan_id is returned.
	pool := worker.NewPool(nil, nil, nil, nil, sc, 1, time.Second)
	h := New(pool, sc)

	resp, err := h.TriggerScan(context.Background(), &scannerv1.TriggerScanRequest{
		TenantId:       "tenant-valid",
		RepositoryName: "org/myrepo",
		ManifestDigest: "sha256:cafebabe",
	})
	if err != nil {
		t.Fatalf("unexpected error for valid request: %v", err)
	}
	if resp.ScanId == "" {
		t.Error("expected non-empty scan_id in response")
	}

	// The scan record should be in the store with at least pending status.
	rec, ok := sc.Get(resp.ScanId)
	if !ok {
		t.Errorf("scan record %q not found in store", resp.ScanId)
	}
	if rec.ManifestDigest != "sha256:cafebabe" {
		t.Errorf("ManifestDigest: got %q, want sha256:cafebabe", rec.ManifestDigest)
	}
}

// newHealthHandlerWithActiveAdapter builds a GRPCHandler wired with an
// adapter registry whose active adapter is named adapterName. It writes a
// fixture binary "scanner-<adapterName>" under a t.TempDir() so
// scannerregistry.New discovers it, then marks it active. Used by the
// GetScannerHealth engine-reachability tests below.
func newHealthHandlerWithActiveAdapter(t *testing.T, adapterName string) *GRPCHandler {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scanner-"+adapterName)
	// Contents are irrelevant — the registry only hashes + stats the file;
	// it never executes it during discovery. 0o755 mirrors the bake-time
	// mode the scanner Dockerfile uses for real adapter binaries.
	if err := os.WriteFile(path, []byte("fixture-binary"), 0o755); err != nil {
		t.Fatalf("write fixture binary: %v", err)
	}

	reg, err := scannerregistry.New(scannerregistry.Options{Dir: dir})
	if err != nil {
		t.Fatalf("scannerregistry.New: %v", err)
	}
	if err := reg.SetActive(path); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	sc := store.New()
	pool := worker.NewPool(nil, nil, nil, nil, sc, 1, time.Second)
	h := New(pool, sc).WithAdapterRegistry(reg)
	return h
}

// TestGetScannerHealth_EngineUnreachable verifies that an active adapter
// with an external engine sidecar pointed at a dead port surfaces
// active_adapter_engine_reachable=false with a non-empty detail string.
func TestGetScannerHealth_EngineUnreachable(t *testing.T) {
	// Active adapter "trivy-adapter" with its engine URL pointed at a dead port.
	t.Setenv("TRIVY_ENGINE_URL", "http://127.0.0.1:1")
	h := newHealthHandlerWithActiveAdapter(t, "trivy-adapter")
	resp, err := h.GetScannerHealth(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetActiveAdapterEngineReachable() {
		t.Fatal("expected engine unreachable=false for a dead trivy-engine")
	}
	if resp.GetActiveAdapterEngineDetail() == "" {
		t.Fatal("expected a detail string on unreachable")
	}
}

// TestGetScannerHealth_GrypeEngineUnreachable mirrors
// TestGetScannerHealth_EngineUnreachable for the grype-adapter added in
// Phase 2 — verifies the adapterEngineURLEnv entry for "grype-adapter"
// (GRYPE_ENGINE_URL) is wired up and probed the same way trivy's is.
func TestGetScannerHealth_GrypeEngineUnreachable(t *testing.T) {
	// Active adapter "grype-adapter" with its engine URL pointed at a dead port.
	t.Setenv("GRYPE_ENGINE_URL", "http://127.0.0.1:1")
	h := newHealthHandlerWithActiveAdapter(t, "grype-adapter")
	resp, err := h.GetScannerHealth(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetActiveAdapterEngineReachable() {
		t.Fatal("expected engine unreachable=false for a dead grype-engine")
	}
	if resp.GetActiveAdapterEngineDetail() == "" {
		t.Fatal("expected a detail string on unreachable")
	}
}

// TestGetScannerHealth_NoEngineAdapter_Reachable verifies that an adapter
// with no external engine sidecar entry (e.g. dev-stub) is always reported
// reachable — there is nothing to probe.
func TestGetScannerHealth_NoEngineAdapter_Reachable(t *testing.T) {
	// dev-stub has no external engine → reachable must be true (nothing to probe).
	h := newHealthHandlerWithActiveAdapter(t, "dev-stub")
	resp, err := h.GetScannerHealth(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetActiveAdapterEngineReachable() {
		t.Fatal("dev-stub has no engine; reachable must be true")
	}
}
