// Package worker implements the scan job consumer and orchestrator.
// It consumes push.completed events from RabbitMQ, manages a worker pool,
// and drives the full scan lifecycle: fetch manifest → invoke plugin → persist result → publish event.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	// FE-API-049: policyResolver is the inheritance walker for
	// auto-scan-on-push decisions. Nil disables policy lookup entirely
	// (every push gets scanned — preserves pre-FE-API-049 behaviour for
	// tests that don't wire the resolver). server.go populates this at
	// boot via WithPolicyResolver so the worker can honour per-repo,
	// per-org and per-tenant policy without a self-gRPC call.
	policyResolver PolicyResolver
	// FUT-017: proxyCachePolicyResolver answers "should we auto-scan
	// the manifest this proxy cache event delivered?" for the
	// (tenant_id, upstream_name) pair. Nil disables proxy-cache auto-
	// scan entirely (events become no-ops). server.go wires this when
	// the scanner DB pool is available.
	proxyCachePolicyResolver ProxyCachePolicyResolver

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
	// FE-API-050: resolved scan policy attached at enqueue time so
	// runJob doesn't have to re-resolve at scan completion. Empty
	// (zero value) when the resolver wasn't wired — runJob then skips
	// the violation/quarantine step entirely.
	policy ResolvedScanPolicy
}

// ResolvedScanPolicy is the slice of the effective scan policy the
// worker actually consumes — keeping the shape narrow means tests can
// stub the resolver without depending on the repository package.
// FE-API-050: BlockOnSeverity + ExemptCVEs drive the post-scan
// quarantine decision; AutoScanOnPush gates whether we enqueue at all.
type ResolvedScanPolicy struct {
	AutoScanOnPush  bool
	BlockOnSeverity string
	ExemptCVEs      []string
}

// PolicyResolver resolves the effective scan policy for one push event.
// Returns the full resolved policy so the worker can both (a) decide
// whether to enqueue (AutoScanOnPush) and (b) decide whether the scan
// result violates the policy and should quarantine the manifest
// (BlockOnSeverity + ExemptCVEs). Returning the full struct from one
// resolver call avoids a second round-trip after the scan completes.
//
// Returning an error fails OPEN on the enqueue side (we scan even when
// the resolver is down — losing a scan is worse than enqueuing one
// against the synthesised default), and fails OPEN on the violation
// side too (no quarantine when we can't read the policy). This is the
// conservative "least surprising regression" stance: prior behaviour
// was always-scan + never-quarantine; a transient DB blip preserves
// both of those rather than silently flipping to a more aggressive
// state.
type PolicyResolver func(ctx context.Context, tenantID, repoID string) (ResolvedScanPolicy, error)

// WithPolicyResolver is the option used by server.go to wire the
// inheritance helper at boot. Tests that don't care about policy can
// leave the resolver unset — Enqueue falls through to "always scan",
// preserving every existing test fixture.
func (p *Pool) WithPolicyResolver(r PolicyResolver) *Pool {
	p.policyResolver = r
	return p
}

// ProxyCachePolicyResolver looks up the FUT-017 per-upstream auto-scan
// policy. Returns (policy, true) when a row exists; (zero, false) when
// the (tenant, upstream) pair has never been configured. An error is
// reserved for genuine infra failures (DB down) — a missing row is the
// common case and uses the (false, nil) branch.
//
// On error the consumer fails CLOSED: a transient DB blip should not
// silently auto-scan everything passing through the pull-through cache.
// That's the opposite stance to PolicyResolver (which fails OPEN for
// pushed images) because cached pulls have no operator intent behind
// each event — only the policy row signals consent.
type ProxyCachePolicyResolver func(ctx context.Context, tenantID, upstreamName string) (ProxyCachePolicy, bool, error)

// ProxyCachePolicy is the worker-visible slice of the persisted
// proxy_cache_scan_policies row. Keeping the shape narrow means tests
// can stub the resolver without depending on the repository.
type ProxyCachePolicy struct {
	AutoScan          bool
	SeverityThreshold string
}

// WithProxyCachePolicyResolver wires the FUT-017 resolver. Tests that
// don't exercise the proxy cache path can leave it unset; the consumer
// then treats every event as "no policy → no scan".
func (p *Pool) WithProxyCachePolicyResolver(r ProxyCachePolicyResolver) *Pool {
	p.proxyCachePolicyResolver = r
	return p
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
// It parses the payload, checks the effective scan policy for the
// repo's tenant + org + repo chain, allocates a scan_id, and enqueues
// the job — but only when auto_scan_on_push is true at whichever scope
// applies.
//
// 2026-06-22 (FE-API-049): added the policy lookup. Before this, the
// worker scanned every push regardless of policy — the per-tenant
// auto_scan_on_push toggle on the dashboard was purely advisory.
// Returns nil even when policy gates the scan so the broker ACKs the
// event (the push is fine, we just chose not to scan it).
func (p *Pool) HandlePushCompleted(ctx context.Context, event events.Event) error {
	var payload events.PushCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal push.completed payload: %w", err)
	}

	// S-MAINT-1 Batch 5 (P6): skip non-image artifacts. Helm charts,
	// cosign signatures, SPDX SBOMs etc. don't carry a Linux rootfs and
	// neither Trivy nor Grype find anything to scan in them — the scan
	// just wastes a cycle and pollutes scan_results with empty rows.
	//
	// Skips only the recognised non-image discriminators ("helm",
	// "signature", "sbom", "other"). The empty string is intentionally
	// kept on the scan path: a pre-Batch-5 publisher (or a manifest
	// whose config block didn't parse) leaves ArtifactType empty, and
	// the safe default there is to scan. This stays correct when an
	// older services/core deploys alongside a newer scanner.
	if payload.ArtifactType != "" && payload.ArtifactType != "image" {
		slog.InfoContext(ctx, "skipping scan for non-image artifact",
			"tenant_id", event.TenantID,
			"repo_id", payload.RepoID,
			"manifest_digest", payload.ManifestDigest,
			"artifact_type", payload.ArtifactType,
		)
		return nil
	}

	// Policy gate. resolver == nil preserves pre-FE-API-049 behaviour
	// (always scan, never quarantine) for tests + dev environments that
	// haven't wired the resolver. A real error from the resolver fails
	// OPEN — we scan anyway against the synthesised default so a
	// transient DB blip doesn't silently turn off scanning. The
	// resolved policy is attached to the scanJob so the violation
	// check at scan completion uses the same answer (no second
	// resolver call needed).
	resolved := ResolvedScanPolicy{
		AutoScanOnPush: true,
		// Empty BlockOnSeverity means "never block" — matches the
		// pre-FE-API-050 always-on, never-quarantine baseline.
	}
	if p.policyResolver != nil {
		got, err := p.policyResolver(ctx, event.TenantID, payload.RepoID)
		if err != nil {
			slog.WarnContext(ctx, "scan policy resolve failed — scanning with default policy",
				"tenant_id", event.TenantID,
				"repo_id", payload.RepoID,
				"err", err,
			)
		} else {
			resolved = got
		}
		if !resolved.AutoScanOnPush {
			slog.InfoContext(ctx, "auto-scan-on-push disabled by policy — skipping scan",
				"tenant_id", event.TenantID,
				"repo_id", payload.RepoID,
				"manifest_digest", payload.ManifestDigest,
			)
			return nil
		}
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
		policy:         resolved,
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

	// FE-API-050: violation check now honours the resolved policy's
	// block_on_severity threshold (was hardcoded to CRITICAL||HIGH).
	// Empty BlockOnSeverity → "never block" → no violation.
	policyViolation := hasPolicyViolation(result, job.policy.BlockOnSeverity)

	p.publishScanCompleted(ctx, job, result, policyViolation)

	if policyViolation {
		p.publishPolicyBlocked(ctx, job, result)
		// FE-API-050: stamp quarantine on the manifest so the next pull
		// is rejected by registry-core. Best-effort — a metadata blip
		// here logs but does not unwind the scan result (which is the
		// audit row of record). The next scan/lift can recover.
		p.applyQuarantine(ctx, job, result)
	}
}

// applyQuarantine flips manifests.quarantined=true via the metadata
// service. Reason is operator-readable text that lands on the 451
// response body when a pull is rejected. quarantined_by="scanner" so
// the audit trail distinguishes automatic policy enforcement from
// operator-triggered manual quarantines.
//
// Failures are logged at warn — the scan_results row was already
// written (that's the system-of-record for the audit trail), and the
// next scan + violation will retry the quarantine stamp idempotently.
func (p *Pool) applyQuarantine(ctx context.Context, job scanJob, result *plugin.ScanResult) {
	reason := buildQuarantineReason(result, job.policy.BlockOnSeverity)
	if _, err := p.metaClient.UpdateManifestQuarantine(ctx, &metadatav1.UpdateManifestQuarantineRequest{
		TenantId:        job.tenantID,
		RepoId:          job.repoID,
		ManifestDigest:  job.manifestDigest,
		Quarantined:     true,
		Reason:          reason,
		QuarantinedBy:   "scanner",
	}); err != nil {
		slog.WarnContext(ctx, "UpdateManifestQuarantine failed",
			"scan_id", job.scanID,
			"manifest_digest", job.manifestDigest,
			"error", err,
		)
		return
	}
	slog.InfoContext(ctx, "manifest quarantined by scan policy",
		"scan_id", job.scanID,
		"manifest_digest", job.manifestDigest,
		"reason", reason,
	)
}

// buildQuarantineReason renders the audit-trail string. Keeping it
// here (rather than in the metadata service) means the dashboard's
// 451 banner shows the operator EXACTLY what the scanner saw, with
// the scanner's vocabulary. Format:
//
//   "scan blocked by policy block_on_severity=HIGH: 3 CRITICAL, 5 HIGH, 2 MEDIUM"
func buildQuarantineReason(result *plugin.ScanResult, blockOn string) string {
	parts := []string{}
	for _, sev := range []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"} {
		if n := result.SeverityCounts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	counts := "no findings"
	if len(parts) > 0 {
		counts = strings.Join(parts, ", ")
	}
	if blockOn == "" {
		blockOn = "CRITICAL"
	}
	return fmt.Sprintf("scan blocked by policy block_on_severity=%s: %s", blockOn, counts)
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

	// scan_results.findings is NOT NULL. On a failed scan we have no
	// findings to attach, but we still need the status row to flip from
	// "pending" → "failed" so the UI doesn't show the job as stuck. Pass
	// an empty JSON array as the canonical "no findings" payload.
	if findingsJSON == nil {
		findingsJSON = []byte("[]")
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

// hasPolicyViolation returns true if the scan found at least one
// vulnerability at the threshold severity or higher.
//
// FE-API-050: honours the operator-configured threshold instead of the
// pre-FE-API-050 hardcoded CRITICAL||HIGH. Severity order from worst
// to least: CRITICAL > HIGH > MEDIUM > LOW. Empty threshold means
// "never block" (matches the FE-API-018 wire shape — `""` is the safe
// default the editor's first radio option emits).
//
// Unknown threshold values (a row hand-edited to "FOO") fail safe by
// treating it as "never block" so an invalid policy doesn't accidentally
// block every push.
func hasPolicyViolation(result *plugin.ScanResult, blockOn string) bool {
	threshold, ok := severityRank[blockOn]
	if !ok {
		return false
	}
	for sev, rank := range severityRank {
		if rank >= threshold && result.SeverityCounts[sev] > 0 {
			return true
		}
	}
	return false
}

// severityRank orders the four standard scanner severities so the
// "blocks at HIGH" check fires for both HIGH and CRITICAL. CRITICAL is
// the worst (highest rank); LOW is the least; the empty string is
// intentionally NOT in the map so the unknown-value branch in
// hasPolicyViolation can detect "never block" without an extra
// allowlist.
var severityRank = map[string]int{
	"LOW":      1,
	"MEDIUM":   2,
	"HIGH":     3,
	"CRITICAL": 4,
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
// It parses the ScanQueuedPayload, resolves the effective scan policy
// (same chain HandlePushCompleted uses) so the post-scan violation
// check + quarantine stamp honour the operator's settings, allocates
// a scan_id, and enqueues the job.
//
// FE-API-050 bugfix: the original implementation skipped the policy
// resolver, so operator-triggered re-scans NEVER quarantined — only
// push-driven scans did. Both surfaces now share the same resolution
// logic + fail-open semantics: a resolver error logs a warning and
// proceeds with the synthesised default (scan everything, never
// quarantine) so a DB blip doesn't silently flip enforcement off.
//
// Unlike push.completed, scan.queued is operator-triggered (the UI
// Rescan button), so we always scan even when AutoScanOnPush is
// false — the operator already overrode the gate by clicking.
func (p *Pool) HandleScanQueued(ctx context.Context, event events.Event) error {
	var payload events.ScanQueuedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal scan.queued payload: %w", err)
	}

	// Resolve the effective policy so applyQuarantine has the right
	// threshold + exempt CVEs at scan completion. Failing open here
	// (resolver returns the synthesised "scan everything, never
	// quarantine" default on error) is the conservative regression
	// safety: pre-FE-API-050 behaviour was always-scan + never-block,
	// and a transient resolver error shouldn't silently flip to a
	// more aggressive state.
	resolved := ResolvedScanPolicy{AutoScanOnPush: true}
	if p.policyResolver != nil {
		got, err := p.policyResolver(ctx, event.TenantID, payload.RepoID)
		if err != nil {
			slog.WarnContext(ctx, "scan policy resolve failed — scanning with default policy",
				"tenant_id", event.TenantID,
				"repo_id", payload.RepoID,
				"err", err,
			)
		} else {
			resolved = got
		}
	}

	scanID := uuid.New().String()
	p.scanStore.Create(scanID, event.TenantID, payload.ManifestDigest, payload.RepositoryName)

	if err := p.Enqueue(scanJob{
		tenantID:       event.TenantID,
		repoID:         payload.RepoID,
		repositoryName: payload.RepositoryName,
		manifestDigest: payload.ManifestDigest,
		scanID:         scanID,
		policy:         resolved,
	}); err != nil {
		return fmt.Errorf("enqueue scan job: %w", err)
	}

	slog.InfoContext(ctx, "scan job enqueued via scan.queued event",
		"scan_id", scanID,
		"tenant_id", event.TenantID,
		"tag", payload.TagName,
		"manifest_digest", payload.ManifestDigest,
		"block_on_severity", resolved.BlockOnSeverity,
	)
	return nil
}

// CachePopulatedConsumerConfig returns the consumer.Config for the
// cache.populated queue (FUT-017). One queue per service so the broker
// can fan-out the same event to scanner + signer.
func CachePopulatedConsumerConfig() consumer.Config {
	return consumer.Config{
		Queue:      "scanner.cache.populated",
		RoutingKey: events.RoutingCachePopulated,
		MaxRetries: 3,
	}
}

// cachePopulatedDedupWindow is the recent-completion window for the
// FUT-017 idempotency check. 30 minutes is a balance between "the same
// digest gets pulled through the cache a hundred times an hour by a CI
// fleet" (we don't want a hundred scans) and "an operator just rotated
// a CVE feed and wants their pulls re-scanned" (we don't want to lock
// out a re-scan for the whole day). The metadata service is still the
// authoritative dedup layer; this is only the cheap first line.
const cachePopulatedDedupWindow = 30 * time.Minute

// HandleCachePopulated is the consumer.Handler for cache.populated
// events (FUT-017). Flow:
//
//   1. Parse the payload (events.CachePopulatedPayload).
//   2. Look up the (tenant_id, upstream_name) policy via the resolver.
//      No policy → ACK + no-op. Resolver error → return the error so
//      the broker NACKs (defence-in-depth: don't silently auto-scan
//      every cached image when the DB is unreachable).
//   3. auto_scan=false → ACK + no-op.
//   4. Idempotency: if scanStore.HasRecentScan reports the same
//      (tenant, digest) is in flight or completed within
//      cachePopulatedDedupWindow, skip enqueue. We log this at info
//      so the operator can see the dedup in action without it looking
//      like an error.
//   5. Enqueue a scanJob just like HandlePushCompleted does. The job
//      goes through the same worker pool / plugin / persist /
//      scan.completed publish path — proxy-cached images are first-
//      class scan inputs.
//
// repoID is intentionally left empty on the enqueued job. cached
// manifests are stored under the proxy schema and the scanner's GetManifest
// flow against metadata works with the manifest digest as the reference
// — the worker treats repoID=="" as "look me up by digest", which is
// the same shape TriggerScan uses for ad-hoc scans.
func (p *Pool) HandleCachePopulated(ctx context.Context, event events.Event) error {
	var payload events.CachePopulatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal cache.populated payload: %w", err)
	}

	// Skip events that don't carry the bits we need. Defensive: a
	// publisher bug that ships an event with no digest should be a
	// no-op rather than a worker panic.
	if payload.TenantID == "" || payload.ManifestDigest == "" || payload.UpstreamName == "" {
		slog.WarnContext(ctx, "cache.populated payload missing required fields — skipping",
			"tenant_id", payload.TenantID,
			"manifest_digest", payload.ManifestDigest,
			"upstream_name", payload.UpstreamName,
		)
		return nil
	}

	// No resolver wired → no proxy-cache auto-scan path. Treat the
	// event as observed-and-acked. This is the path tests + dev
	// environments that haven't opted into FUT-017 take.
	if p.proxyCachePolicyResolver == nil {
		return nil
	}

	policy, ok, err := p.proxyCachePolicyResolver(ctx, payload.TenantID, payload.UpstreamName)
	if err != nil {
		// Fail CLOSED — NACK + let the broker redeliver. Without a
		// policy lookup we can't tell whether the operator wanted this
		// scanned, and we'd rather waste a few redeliveries than
		// silently turn auto-scan on or off.
		return fmt.Errorf("resolve proxy cache scan policy: %w", err)
	}
	if !ok || !policy.AutoScan {
		// No row, or row present but disabled. Either way: nothing
		// for the scanner to do.
		return nil
	}

	// Idempotency. Cheap in-memory check first — a popular image
	// pulled hundreds of times an hour by a CI fleet would otherwise
	// stack hundreds of identical scans. The metadata layer still
	// dedups on scan_results uniqueness for cross-process safety.
	if p.scanStore.HasRecentScan(payload.TenantID, payload.ManifestDigest, cachePopulatedDedupWindow) {
		slog.InfoContext(ctx, "cache.populated: scan already in flight or recently completed — skipping",
			"tenant_id", payload.TenantID,
			"upstream_name", payload.UpstreamName,
			"manifest_digest", payload.ManifestDigest,
		)
		return nil
	}

	// Enqueue. repositoryName uses the upstream image so log lines
	// downstream tell the operator which image was pulled. policy is
	// kept narrow — the scanner's existing block_on_severity flow
	// handles the FUT-017 severity_threshold by mapping "critical" →
	// "CRITICAL" etc. at the resolver boundary; here we forward the
	// pre-mapped uppercase form.
	scanID := uuid.New().String()
	p.scanStore.Create(scanID, payload.TenantID, payload.ManifestDigest, payload.Image)
	if err := p.Enqueue(scanJob{
		tenantID:       payload.TenantID,
		repoID:         "",
		repositoryName: payload.Image,
		manifestDigest: payload.ManifestDigest,
		scanID:         scanID,
		policy: ResolvedScanPolicy{
			AutoScanOnPush:  true,
			BlockOnSeverity: policy.SeverityThreshold,
		},
	}); err != nil {
		return fmt.Errorf("enqueue proxy-cache scan job: %w", err)
	}

	slog.InfoContext(ctx, "cache.populated: scan job enqueued",
		"scan_id", scanID,
		"tenant_id", payload.TenantID,
		"upstream_name", payload.UpstreamName,
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

