// Package worker implements the scan job consumer and orchestrator.
// It consumes push.completed events from RabbitMQ, manages a worker pool,
// and drives the full scan lifecycle: fetch manifest → invoke plugin → persist result → publish event.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// ociManifest is the minimal subset of an OCI/Docker manifest we need to extract layer digests.
type ociManifest struct {
	Layers []struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

// Pool manages a fixed-size pool of scan worker goroutines.
type Pool struct {
	scanner    plugin.Scanner
	metaClient metadatav1.MetadataServiceClient
	storageConn *grpc.ClientConn
	pub        *publisher.Publisher
	scanStore  *store.Store
	jobs       chan scanJob
	timeout    time.Duration
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
func NewPool(
	scanner plugin.Scanner,
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	pub *publisher.Publisher,
	scanStore *store.Store,
	workerCount int,
	jobTimeout time.Duration,
) *Pool {
	return &Pool{
		scanner:     scanner,
		metaClient:  metadatav1.NewMetadataServiceClient(metaConn),
		storageConn: storageConn,
		pub:         pub,
		scanStore:   scanStore,
		jobs:        make(chan scanJob, workerCount*2),
		timeout:     jobTimeout,
	}
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
func (p *Pool) runJob(ctx context.Context, job scanJob) {
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
		p.persistScanStatus(ctx, job, "failed", nil, nil)
		return
	}

	p.scanStore.SetComplete(job.scanID, result.SeverityCounts)
	p.persistScanStatus(ctx, job, "complete", result.SeverityCounts, marshalFindings(result))

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
	return p.scanner.Scan(ctx, plugin.ScanRequest{
		TenantID:       job.tenantID,
		RepositoryName: job.repositoryName,
		ManifestDigest: job.manifestDigest,
		Layers:         layers,
		StorageFetcher: fetcher,
	})
}

// persistScanStatus calls metadata.UpdateScanStatus to persist the final result.
func (p *Pool) persistScanStatus(ctx context.Context, job scanJob, status string, counts map[string]int, findingsJSON []byte) {
	countsProto := make(map[string]int32, len(counts))
	for k, v := range counts {
		countsProto[k] = int32(v)
	}

	_, err := p.metaClient.UpdateScanStatus(ctx, &metadatav1.UpdateScanStatusRequest{
		ScanId:         job.scanID,
		TenantId:       job.tenantID,
		Status:         status,
		FindingsJson:   findingsJSON,
		SeverityCounts: countsProto,
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

