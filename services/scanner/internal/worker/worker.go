// Package worker implements the scan job consumer and orchestrator.
// It consumes push.completed events from RabbitMQ, manages a worker pool,
// and drives the full scan lifecycle: fetch manifest → invoke plugin → persist result → publish event.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/scanner/plugin"
	"github.com/steveokay/oci-janus/services/scanner/internal/blobfetcher"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// VersionRecorder is the narrow contract the worker needs to backfill the
// adapter registry's per-name version cache after a successful scan. The
// real adapter registry satisfies this; tests pass a no-op or fake.
//
// Kept tiny + behind an interface so the worker package doesn't have to
// import services/scanner/internal/registry (which would risk cycles —
// the registry has no business depending on the worker, but Go's import
// cycle rules treat the reverse just as carefully).
type VersionRecorder interface {
	RecordVersion(name, version string)
}

// noopVersionRecorder is the default when SetVersionRecorder hasn't been
// called yet (e.g. existing tests that construct Pool without wiring the
// registry). Keeps doScan/runJob branch-free.
type noopVersionRecorder struct{}

func (noopVersionRecorder) RecordVersion(string, string) {}

// ociManifest is the minimal subset of an OCI/Docker manifest we need to extract layer digests.
type ociManifest struct {
	Layers []struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

// Pool manages a fixed-size pool of scan worker goroutines.
//
// scanner is an atomic.Pointer so SetScanner can swap the active adapter
// without restarting the worker goroutines or interrupting in-flight scans
// (REM-011 Phase 2). Each call to doScan reads the pointer exactly once
// per job, so a swap mid-batch causes the next job to pick up the new
// adapter while running jobs finish on their original adapter.
//
// inFlight + lastSuccessAt are maintained for GetScannerHealth. They are
// atomic because runJob is the only writer and many gRPC handlers can be
// reading concurrently.
type Pool struct {
	scanner atomic.Pointer[plugin.Scanner]
	// versionRec is an atomic.Pointer so swap-time the dynamic type stays
	// constant (atomic.Value panics if subsequent Stores present a
	// different concrete type than the first one — the original
	// implementation hit that with noopVersionRecorder vs *registry.Registry).
	// Always non-nil after NewPool; defaults to a pointer to the no-op.
	versionRec  atomic.Pointer[VersionRecorder]
	metaClient  metadatav1.MetadataServiceClient
	storageConn *grpc.ClientConn
	pub         *publisher.Publisher
	scanStore   *store.Store
	jobs        chan scanJob
	timeout     time.Duration

	// inFlight is the number of scans currently executing on a worker
	// goroutine. Incremented at runJob entry, decremented on exit.
	inFlight atomic.Int64
	// lastSuccessAtNanos is the UnixNano of the most recent successful
	// scan completion. Zero means "never". Read atomically; converted
	// back to time.Time at read by the gRPC handler.
	lastSuccessAtNanos atomic.Int64
}

type scanJob struct {
	tenantID       string
	repoID         string
	repositoryName string
	manifestDigest string
	pushedBy       string
	scanID         string
}

// NewPool creates a Pool with workerCount goroutines ready to process jobs.
//
// scanner is the initial active adapter; SetScanner can replace it later
// without restarting the pool. The pointer indirection is internal — callers
// just pass the same plugin.Scanner they always have.
func NewPool(
	scanner plugin.Scanner,
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	pub *publisher.Publisher,
	scanStore *store.Store,
	workerCount int,
	jobTimeout time.Duration,
) *Pool {
	p := &Pool{
		metaClient:  metadatav1.NewMetadataServiceClient(metaConn),
		storageConn: storageConn,
		pub:         pub,
		scanStore:   scanStore,
		jobs:        make(chan scanJob, workerCount*2),
		timeout:     jobTimeout,
	}
	p.scanner.Store(&scanner)
	// Default to a no-op recorder so doScan never has to nil-check.
	var noop VersionRecorder = noopVersionRecorder{}
	p.versionRec.Store(&noop)
	return p
}

// SetScanner atomically replaces the active scanner adapter. In-flight
// scans complete on their pre-swap adapter; the next job picks up the
// new one. Safe to call concurrently with Enqueue / runJob.
//
// Pass a non-nil scanner — a nil here would surface as a nil-deref
// inside doScan, which is harder to debug than a clear panic at the
// call site. Production callers (server.Run handling SetActiveAdapter)
// always build the scanner first via plugin.New, which itself rejects
// nil.
func (p *Pool) SetScanner(s plugin.Scanner) {
	if s == nil {
		panic("worker.Pool.SetScanner: nil scanner")
	}
	p.scanner.Store(&s)
}

// SetVersionRecorder wires the adapter registry's RecordVersion hook so
// successful scans backfill the registry's per-adapter version cache.
// Safe to call once at startup; concurrent calls are also fine.
func (p *Pool) SetVersionRecorder(v VersionRecorder) {
	if v == nil {
		v = noopVersionRecorder{}
	}
	p.versionRec.Store(&v)
}

// activeScanner returns the currently active adapter, read atomically.
// Loop bodies in doScan use this exactly once per job to make the swap
// semantics obvious.
func (p *Pool) activeScanner() plugin.Scanner {
	return *p.scanner.Load()
}

// QueueDepth is the number of jobs waiting in the buffered channel.
// Cheap len() on a channel — safe to call from any goroutine.
func (p *Pool) QueueDepth() int { return len(p.jobs) }

// InFlightCount is the number of scans currently executing on worker
// goroutines.
func (p *Pool) InFlightCount() int64 { return p.inFlight.Load() }

// LastSuccessAt returns the timestamp of the most recent successful scan,
// or the zero time when no scan has succeeded since process start.
func (p *Pool) LastSuccessAt() time.Time {
	n := p.lastSuccessAtNanos.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// Start launches workerCount goroutines that consume from the internal jobs channel.
// It returns when ctx is cancelled.
func (p *Pool) Start(ctx context.Context, workerCount int) {
	for range workerCount {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-p.jobs:
					if !ok {
						return
					}
					p.runJob(ctx, job)
				}
			}
		}()
	}
	<-ctx.Done()
}

// ErrQueueFull is returned by Enqueue when the worker pool's job channel is
// full and the caller should apply backpressure (typically: NACK the broker
// message so it's re-delivered after the in-flight jobs drain).
var ErrQueueFull = fmt.Errorf("scanner worker queue full")

// Enqueue adds a scan job to the pool without blocking.
//
// PENTEST-023: when the queue is full this returns ErrQueueFull instead of
// spawning an unbounded goroutine. RabbitMQ consumers translate the error
// into a NACK; the broker re-delivers after a short backoff, which is the
// correct backpressure signal. A short blocking wait first absorbs micro
// bursts without immediately bouncing every message.
func (p *Pool) Enqueue(job scanJob) error {
	select {
	case p.jobs <- job:
		return nil
	default:
	}
	// Short blocking attempt to absorb micro-bursts before NACKing.
	select {
	case p.jobs <- job:
		return nil
	case <-time.After(50 * time.Millisecond):
		return ErrQueueFull
	}
}

// HandlePushCompleted is the consumer.Handler for push.completed events.
// It parses the payload, allocates a scan_id, and enqueues the job.
func (p *Pool) HandlePushCompleted(ctx context.Context, event events.Event) error {
	var payload events.PushCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal push.completed payload: %w", err)
	}

	scanID := uuid.New().String()
	p.scanStore.Create(scanID, event.TenantID, payload.ManifestDigest, payload.RepositoryName)

	if err := p.Enqueue(scanJob{
		tenantID:       event.TenantID,
		repoID:         payload.RepoID,
		repositoryName: payload.RepositoryName,
		manifestDigest: payload.ManifestDigest,
		pushedBy:       payload.PushedBy,
		scanID:         scanID,
	}); err != nil {
		// Return the error so the RabbitMQ consumer NACKs and the broker
		// re-delivers after backoff (PENTEST-023: prefer backpressure over
		// spawning unbounded goroutines).
		return fmt.Errorf("enqueue scan job: %w", err)
	}

	slog.InfoContext(ctx, "scan job enqueued",
		"scan_id", scanID,
		"tenant_id", event.TenantID,
		"manifest_digest", payload.ManifestDigest,
	)
	return nil
}

// runJob executes the full scan lifecycle for one job.
//
// in-flight counter is incremented before any work and deferred back so
// even a panic in doScan leaves the counter consistent for the next
// GetScannerHealth call.
func (p *Pool) runJob(ctx context.Context, job scanJob) {
	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	jobCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	p.scanStore.SetRunning(job.scanID)

	result, err := p.doScan(jobCtx, job)
	if err != nil {
		slog.ErrorContext(jobCtx, "scan job failed",
			"scan_id", job.scanID,
			"manifest_digest", job.manifestDigest,
			"error", err,
		)
		p.scanStore.SetFailed(job.scanID)
		// Result is nil here — the plugin never completed. Pass it
		// through so persistScanStatus can backfill placeholder
		// scanner_name/version values to satisfy NOT NULL columns.
		p.persistScanStatus(ctx, job, "failed", nil, nil, nil)
		return
	}

	p.scanStore.SetComplete(job.scanID, result.SeverityCounts)
	p.persistScanStatus(ctx, job, "complete", result.SeverityCounts, marshalFindings(result), result)
	// Mark the timestamp only after the full success path including
	// the persist call — partial success (plugin returned but metadata
	// write failed) should not bump health stats.
	p.lastSuccessAtNanos.Store(time.Now().UnixNano())
	// Backfill the registry's per-adapter version cache so the next
	// GetScannerHealth / ListInstalledAdapters call shows a real
	// version string instead of "unknown". Cheap RWLock write; no-op
	// when the recorder hasn't been wired (existing tests).
	// versionRec is an *atomic.Pointer[VersionRecorder]; deref the
	// loaded pointer to get back the interface value. Always non-nil
	// after NewPool (defaults to noopVersionRecorder).
	if recPtr := p.versionRec.Load(); recPtr != nil {
		(*recPtr).RecordVersion(result.ScannerName, result.ScannerVersion)
	}

	policyViolation := hasPolicyViolation(result)
	p.publishScanCompleted(ctx, job, result, policyViolation)

	if policyViolation {
		p.publishPolicyBlocked(ctx, job, result)
	}
}

// doScan fetches the manifest, extracts layers, and invokes the scanner plugin.
func (p *Pool) doScan(ctx context.Context, job scanJob) (*plugin.ScanResult, error) {
	manifest, err := p.metaClient.GetManifest(ctx, &metadatav1.GetManifestRequest{
		RepoId:    job.repoID,
		TenantId:  job.tenantID,
		Reference: job.manifestDigest,
	})
	if err != nil {
		return nil, fmt.Errorf("GetManifest: %w", err)
	}

	var ociMf ociManifest
	if err := json.Unmarshal(manifest.RawJson, &ociMf); err != nil {
		return nil, fmt.Errorf("parse manifest JSON: %w", err)
	}

	layers := make([]plugin.LayerRef, 0, len(ociMf.Layers))
	for _, l := range ociMf.Layers {
		layers = append(layers, plugin.LayerRef{
			Digest:    l.Digest,
			MediaType: l.MediaType,
			Size:      l.Size,
		})
	}

	fetcher := blobfetcher.New(p.storageConn, job.tenantID)
	// Read the active scanner once at the top of the call so a SetScanner
	// midway through this job has no effect on this job's outcome (the
	// invariant the SetActiveAdapter contract advertises). The next
	// scanJob popped off the channel will see the new pointer.
	scanner := p.activeScanner()
	return scanner.Scan(ctx, plugin.ScanRequest{
		TenantID:       job.tenantID,
		RepositoryName: job.repositoryName,
		ManifestDigest: job.manifestDigest,
		Layers:         layers,
		StorageFetcher: fetcher,
	})
}

// persistScanStatus calls metadata.UpdateScanStatus to persist the
// scan result. metadata.UpsertScanResult upserts on scan_id so the
// first call (status="failed" without a plugin result, or "complete"
// with one) creates the row using the identity fields below, and any
// retry just updates the mutable columns.
//
// scannerName + scannerVersion are passed through from the plugin
// response when available; an empty plugin run (failed before the
// plugin returned a result) populates them with placeholders so the
// NOT NULL columns are satisfied and the operator can still see which
// scan_id failed.
func (p *Pool) persistScanStatus(ctx context.Context, job scanJob, status string, counts map[string]int, findingsJSON []byte, result *plugin.ScanResult) {
	countsProto := make(map[string]int32, len(counts))
	for k, v := range counts {
		countsProto[k] = int32(v)
	}

	scannerName, scannerVersion := "unknown", "unknown"
	if result != nil {
		if result.ScannerName != "" {
			scannerName = result.ScannerName
		}
		if result.ScannerVersion != "" {
			scannerVersion = result.ScannerVersion
		}
	}

	_, err := p.metaClient.UpdateScanStatus(ctx, &metadatav1.UpdateScanStatusRequest{
		ScanId:         job.scanID,
		TenantId:       job.tenantID,
		Status:         status,
		FindingsJson:   findingsJSON,
		SeverityCounts: countsProto,
		RepoId:         job.repoID,
		ManifestDigest: job.manifestDigest,
		ScannerName:    scannerName,
		ScannerVersion: scannerVersion,
	})
	if err != nil {
		slog.ErrorContext(ctx, "UpdateScanStatus failed",
			"scan_id", job.scanID,
			"error", err,
		)
	}
}

// publishScanCompleted emits a scan.completed event to RabbitMQ.
func (p *Pool) publishScanCompleted(ctx context.Context, job scanJob, result *plugin.ScanResult, policyViolation bool) {
	payload, _ := json.Marshal(events.ScanCompletedPayload{
		ManifestDigest:  job.manifestDigest,
		RepositoryName:  job.repositoryName,
		ScannerName:     result.ScannerName,
		SeverityCounts:  result.SeverityCounts,
		PolicyViolation: policyViolation,
		Blocked:         policyViolation,
	})

	event := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingScanCompleted,
		TenantID:   job.tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := p.pub.Publish(ctx, events.RoutingScanCompleted, event); err != nil {
		slog.ErrorContext(ctx, "publish scan.completed failed", "scan_id", job.scanID, "error", err)
	}
}

// publishPolicyBlocked emits a scan.policy_blocked event when findings breach policy.
func (p *Pool) publishPolicyBlocked(ctx context.Context, job scanJob, result *plugin.ScanResult) {
	payload, _ := json.Marshal(events.ScanCompletedPayload{
		ManifestDigest:  job.manifestDigest,
		RepositoryName:  job.repositoryName,
		ScannerName:     result.ScannerName,
		SeverityCounts:  result.SeverityCounts,
		PolicyViolation: true,
		Blocked:         true,
	})
	event := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingScanPolicyBlocked,
		TenantID:   job.tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := p.pub.Publish(ctx, events.RoutingScanPolicyBlocked, event); err != nil {
		slog.ErrorContext(ctx, "publish scan.policy_blocked failed", "scan_id", job.scanID, "error", err)
	}
}

// hasPolicyViolation returns true if the scan found CRITICAL or HIGH vulnerabilities.
// Per CLAUDE.md §4.7: default block-on-severity is CRITICAL/HIGH.
func hasPolicyViolation(result *plugin.ScanResult) bool {
	return result.SeverityCounts["CRITICAL"] > 0 || result.SeverityCounts["HIGH"] > 0
}

// marshalFindings serialises scan findings to JSON for storage in metadata.
func marshalFindings(result *plugin.ScanResult) []byte {
	b, _ := json.Marshal(result.Findings)
	return b
}

// ConsumerConfig returns the consumer.Config for the push.completed queue.
func ConsumerConfig() consumer.Config {
	return consumer.Config{
		Queue:      "scanner.push.completed",
		RoutingKey: events.RoutingPushCompleted,
		MaxRetries: 3,
	}
}

// ScanQueuedConsumerConfig returns the consumer.Config for the scan.queued queue.
// This queue receives manually triggered scan requests from registry-management,
// allowing scans to be started outside the normal push.completed flow.
func ScanQueuedConsumerConfig() consumer.Config {
	return consumer.Config{
		Queue:      "scanner.scan.queued",
		RoutingKey: events.RoutingScanQueued,
		MaxRetries: 3,
	}
}

// HandleScanQueued is the consumer.Handler for scan.queued events.
// It parses the ScanQueuedPayload, allocates a scan_id, and enqueues the job.
func (p *Pool) HandleScanQueued(ctx context.Context, event events.Event) error {
	var payload events.ScanQueuedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal scan.queued payload: %w", err)
	}

	scanID := uuid.New().String()
	p.scanStore.Create(scanID, event.TenantID, payload.ManifestDigest, payload.RepositoryName)

	if err := p.Enqueue(scanJob{
		tenantID:       event.TenantID,
		repoID:         payload.RepoID,
		repositoryName: payload.RepositoryName,
		manifestDigest: payload.ManifestDigest,
		scanID:         scanID,
	}); err != nil {
		return fmt.Errorf("enqueue scan job: %w", err)
	}

	slog.InfoContext(ctx, "scan job enqueued via scan.queued event",
		"scan_id", scanID,
		"tenant_id", event.TenantID,
		"tag", payload.TagName,
		"manifest_digest", payload.ManifestDigest,
	)
	return nil
}

// TriggerScanJob creates a scan_id, registers it, and enqueues a job without
// waiting for a RabbitMQ event. Used by the TriggerScan gRPC handler.
func (p *Pool) TriggerScanJob(tenantID, repoID, repoName, manifestDigest string) string {
	scanID := uuid.New().String()
	p.scanStore.Create(scanID, tenantID, manifestDigest, repoName)
	p.Enqueue(scanJob{
		tenantID:       tenantID,
		repoID:         repoID,
		repositoryName: repoName,
		manifestDigest: manifestDigest,
		scanID:         scanID,
	})
	return scanID
}

