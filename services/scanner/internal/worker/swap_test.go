// Package worker — REM-011 Phase 2 atomic adapter-swap unit tests.
//
// These tests exercise the contract that SetScanner can swap the active
// scanner mid-life without touching the worker goroutines, and that the
// concurrent reader (activeScanner) sees the new pointer immediately.
// No grpc, no DB — just the atomic.Pointer dance.
package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/libs/scanner/plugin"
)

// fakeScanner is a minimal plugin.Scanner that records the name it was
// asked to scan under. Used to assert which adapter handled a given job.
type fakeScanner struct {
	name string
	// scanCalls counts how many times Scan was invoked. Atomic because
	// the test runs Scan from multiple goroutines.
	scanCalls atomic.Int64
}

func (f *fakeScanner) Name() string    { return f.name }
func (f *fakeScanner) Version() string { return "test" }

func (f *fakeScanner) Scan(_ context.Context, _ plugin.ScanRequest) (*plugin.ScanResult, error) {
	f.scanCalls.Add(1)
	return &plugin.ScanResult{
		ScannerName:    f.name,
		ScannerVersion: "test",
		SeverityCounts: map[string]int{},
	}, nil
}

// TestPool_SetScanner_swapsActivePointer verifies activeScanner reflects
// the new pointer after SetScanner returns, and the previous instance is
// no longer reachable through activeScanner.
func TestPool_SetScanner_swapsActivePointer(t *testing.T) {
	stub := &fakeScanner{name: "dev-stub"}
	trivy := &fakeScanner{name: "trivy-adapter"}

	p := &Pool{}
	p.scanner.Store((*plugin.Scanner)(scannerPtr(stub)))

	if got := p.activeScanner().Name(); got != "dev-stub" {
		t.Fatalf("initial activeScanner = %q, want dev-stub", got)
	}
	p.SetScanner(trivy)
	if got := p.activeScanner().Name(); got != "trivy-adapter" {
		t.Errorf("post-swap activeScanner = %q, want trivy-adapter", got)
	}
}

// TestPool_SetScanner_concurrentReadersSeeNewPointer hammers activeScanner
// from N goroutines while SetScanner runs in the main goroutine. No
// race-detector flags should fire under `go test -race`, and every
// reader after the swap should see the new adapter at some point.
func TestPool_SetScanner_concurrentReadersSeeNewPointer(t *testing.T) {
	stub := &fakeScanner{name: "dev-stub"}
	trivy := &fakeScanner{name: "trivy-adapter"}

	p := &Pool{}
	p.scanner.Store((*plugin.Scanner)(scannerPtr(stub)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	sawTrivy := atomic.Bool{}
	const readers = 32
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if p.activeScanner().Name() == "trivy-adapter" {
					sawTrivy.Store(true)
					return
				}
			}
		}()
	}

	// Let readers spin briefly on stub then swap to trivy.
	time.Sleep(5 * time.Millisecond)
	p.SetScanner(trivy)

	// Give the readers a window to observe the swap.
	deadline := time.After(500 * time.Millisecond)
	for !sawTrivy.Load() {
		select {
		case <-deadline:
			t.Fatal("no reader observed the post-swap scanner within 500ms")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	wg.Wait()
}

// TestPool_SetVersionRecorder_defaultsToNoop verifies the constructor
// invariant: a fresh Pool's versionRec is the no-op so doScan never
// has to nil-check. Tests construct Pool directly (no NewPool) so we
// also assert SetVersionRecorder(nil) reinstates the no-op.
func TestPool_SetVersionRecorder_nilFallsBackToNoop(t *testing.T) {
	p := &Pool{}
	p.SetVersionRecorder(nil)
	rec := p.versionRec.Load()
	if rec == nil {
		t.Fatal("versionRec should never be nil after SetVersionRecorder")
	}
	// no-op call must not panic.
	(*rec).RecordVersion("trivy-adapter", "0.55.0")
}

// scannerPtr is a tiny helper that promotes a concrete fakeScanner to
// the plugin.Scanner interface and returns a pointer to that interface
// value — the shape atomic.Pointer[plugin.Scanner] expects.
func scannerPtr(s plugin.Scanner) *plugin.Scanner {
	return &s
}
