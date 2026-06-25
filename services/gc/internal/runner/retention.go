// Package runner — FE-API-040 retention executor.
//
// Two execution modes:
//
//	retention        — soft-delete pass. Calls metadata.GetEffectiveRetentionPolicy
//	                   + metadata.EvaluateRetention, then stamps every matched
//	                   manifest's retention_pending_delete_at via
//	                   metadata.MarkManifestRetentionPending. No data is removed
//	                   yet — the grace window protects against operator mistakes.
//
//	retention_grace  — finaliser. Lists manifests whose pending timestamp has
//	                   ridden out the configured grace window and hard-deletes
//	                   them via metadata.DeleteManifest (which already cascades
//	                   to the orphan-blob path).
//
// Both modes thread through the dispatcher in runner.go so the cron loop and
// the ad-hoc TriggerRetentionRun RPC share one execution surface — same
// gc_runs lifecycle, same metrics, same logging.
//
// FE-API-041 wired the publish helpers through libs/rabbitmq/publisher. A nil
// publisher (no RABBITMQ_URL configured) is a no-op so the executor still
// drains queued runs in a dev install without a broker; publish errors are
// logged but never fail the run — events are best-effort, the gc_runs row
// is the system of record.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// RetentionConfig bundles the knobs the executor needs at construction time.
// Centralising them keeps the runner.go constructor lean and lets the cron
// dispatcher swap the grace window for tests without touching the wider
// PersistedRunner surface.
type RetentionConfig struct {
	// GraceWindow is the duration a manifest must spend in the pending state
	// before the retention_grace sweep is allowed to hard-delete it.
	// Defaults to 7 days when zero.
	GraceWindow time.Duration
	// MaxGraceCandidatesPerRun caps the number of manifests one
	// retention_grace pass will hard-delete. 1000 by default. Bounds the
	// work per tick so the cross-tenant grace ticker doesn't starve other
	// gc work on a busy install.
	MaxGraceCandidatesPerRun int
	// MaxEvaluatedCandidates caps the would_delete list pulled from
	// EvaluateRetention during a retention (soft-delete) sweep. 50k by
	// default — large enough that a typical sweep finishes in one pass,
	// small enough to avoid OOMs on a catastrophically broad policy.
	MaxEvaluatedCandidates int
}

// defaultRetentionConfig returns the FE-API-040 baseline knobs. Constructor
// callers can override individual fields without re-typing the defaults.
func defaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		GraceWindow:              7 * 24 * time.Hour,
		MaxGraceCandidatesPerRun: 1000,
		MaxEvaluatedCandidates:   50_000,
	}
}

// SetRetentionConfig swaps the retention knobs. Tests use this to shrink the
// grace window so they don't have to wait 7 days for a row to qualify. The
// caller is responsible for calling before CronLoop starts; mutating after
// the cron loop has spawned is racy and unsupported.
func (p *PersistedRunner) SetRetentionConfig(cfg RetentionConfig) {
	if cfg.GraceWindow > 0 {
		p.retention.GraceWindow = cfg.GraceWindow
	}
	if cfg.MaxGraceCandidatesPerRun > 0 {
		p.retention.MaxGraceCandidatesPerRun = cfg.MaxGraceCandidatesPerRun
	}
	if cfg.MaxEvaluatedCandidates > 0 {
		p.retention.MaxEvaluatedCandidates = cfg.MaxEvaluatedCandidates
	}
}

// MetadataClient is the narrow contract the retention executor needs on the
// metadata gRPC stub. Mirrors the relevant subset of metadatav1.MetadataServiceClient
// — *metadatav1.MetadataServiceClient satisfies the interface without an
// adapter, and tests can drop in a hand-written fake without standing up
// bufconn.
type MetadataClient interface {
	GetEffectiveRetentionPolicy(ctx context.Context, req *metadatav1.GetEffectiveRetentionPolicyRequest, opts ...grpc.CallOption) (*metadatav1.EffectiveRetentionPolicy, error)
	EvaluateRetention(ctx context.Context, req *metadatav1.EvaluateRetentionRequest, opts ...grpc.CallOption) (*metadatav1.EvaluateRetentionResponse, error)
	MarkManifestRetentionPending(ctx context.Context, req *metadatav1.MarkManifestRetentionPendingRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	ListPendingDeleteManifests(ctx context.Context, req *metadatav1.ListPendingDeleteManifestsRequest, opts ...grpc.CallOption) (*metadatav1.ListPendingDeleteManifestsResponse, error)
	DeleteManifest(ctx context.Context, req *metadatav1.DeleteManifestRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

// StorageClient is the narrow contract for blob deletion during a
// retention_grace finaliser. Currently retention_grace defers blob cleanup
// to the orphan-blob path (metadata.DeleteManifest cascades through
// blob_links), so this surface is unused today — keeping the interface in
// place lets FE-API-041+ wire direct storage deletes without another
// runner refactor.
type StorageClient interface {
	DeleteBlob(ctx context.Context, req *storagev1.DeleteBlobRequest, opts ...grpc.CallOption) (*storagev1.DeleteBlobResponse, error)
}

// ─── RunRetention (soft-delete pass) ────────────────────────────────────────

// RunRetention is the executor for mode=retention. Looks up the effective
// policy for the run's repo_id, dry-runs it via EvaluateRetention, and
// stamps retention_pending_delete_at on every selected manifest.
//
// Behaviour matrix:
//
//   - No effective policy on the repo → succeed with manifests_marked=0.
//     Recorded as "no policy" in error_message (informational, not an error).
//   - Effective policy's preview_until > NOW → skip marking, record an
//     informational error_message. This is the FE-API-038 preview window guard.
//   - Effective policy disabled (per-repo enabled=false) → no-op like
//     "no policy" — the executor never marks a manifest under a disabled
//     policy.
//   - Otherwise call EvaluateRetention with a high max_delete_results cap and
//     mark every would_delete entry via MarkManifestRetentionPending.
//
// Errors mid-marking (a single MarkPending fails) are logged but do NOT abort
// the sweep — the next attempt will pick up the surviving manifests because
// MarkPending is idempotent on the second pass.
func (p *PersistedRunner) RunRetention(ctx context.Context, run *repository.GCRun) error {
	if p.metaClient == nil {
		return p.fail(ctx, run.RunID, "metadata client not configured")
	}
	if run.RepoID == uuid.Nil {
		return p.fail(ctx, run.RunID, "retention run missing repo_id")
	}

	tenantID := run.TenantID.String()
	repoID := run.RepoID.String()

	eff, err := p.metaClient.GetEffectiveRetentionPolicy(ctx, &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: tenantID,
		RepoId:   repoID,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// No policy anywhere — record the informational outcome and exit.
			slog.InfoContext(ctx, "retention: no policy for repo, skipping",
				"run_id", run.RunID, "repo_id", repoID, "tenant_id", tenantID)
			return p.finalize(ctx, run.RunID, 0, 0, 0, "no policy")
		}
		return p.fail(ctx, run.RunID, fmt.Sprintf("get effective policy: %v", err))
	}

	policy := eff.GetPolicy()
	if policy == nil || !policy.GetEnabled() {
		slog.InfoContext(ctx, "retention: policy disabled, skipping",
			"run_id", run.RunID, "repo_id", repoID)
		return p.finalize(ctx, run.RunID, 0, 0, 0, "policy disabled")
	}

	// Preview window guard (FE-API-038). When the operator's saved policy is
	// still in its dry-run period, the executor MUST NOT delete anything —
	// the whole point of the window is to let the operator inspect what the
	// policy would do before enforcement starts.
	if pu := policy.GetPreviewUntil(); pu != nil {
		previewUntil := pu.AsTime()
		if previewUntil.After(time.Now()) {
			slog.InfoContext(ctx, "retention: preview window active, skipping",
				"run_id", run.RunID, "preview_until", previewUntil)
			// Still evaluate so subscribers see "we WOULD have deleted X". The
			// preview event carries PolicyPreviewUntil so consumers can render
			// "X manifests would be deleted after <date>".
			previewEval, evalErr := p.metaClient.EvaluateRetention(ctx, &metadatav1.EvaluateRetentionRequest{
				TenantId:            tenantID,
				RepoId:              repoID,
				Candidate:           policyToCandidate(policy),
				MaxDeleteResults:    int32(p.retention.MaxEvaluatedCandidates),
				MaxProtectedResults: 0,
			})
			if evalErr == nil && previewEval != nil {
				p.publishRetentionEvaluated(ctx, run, "retention",
					previewEval.GetTotalCount(), previewEval.GetTotalBytes(), &previewUntil)
			}
			return p.finalize(ctx, run.RunID, 0, 0, 0,
				fmt.Sprintf("preview window active until %s", previewUntil.UTC().Format(time.RFC3339)))
		}
	}

	// Convert the persisted policy back into a candidate so the same
	// EvaluateRetention path used by the dry-run UI drives the executor.
	// Single source of truth — no parallel evaluator path.
	maxDelete := int32(p.retention.MaxEvaluatedCandidates)
	resp, err := p.metaClient.EvaluateRetention(ctx, &metadatav1.EvaluateRetentionRequest{
		TenantId:            tenantID,
		RepoId:              repoID,
		Candidate:           policyToCandidate(policy),
		MaxDeleteResults:    maxDelete,
		MaxProtectedResults: 0, // executor doesn't need the protected list.
	})
	if err != nil {
		return p.fail(ctx, run.RunID, fmt.Sprintf("evaluate retention: %v", err))
	}

	// FE-API-041: fire retention.evaluated BEFORE marking so subscribers see
	// "this is about to happen" and can render the projection. Best-effort —
	// publish errors don't abort the sweep.
	p.publishRetentionEvaluated(ctx, run, "retention",
		resp.GetTotalCount(), resp.GetTotalBytes(), nil)

	candidates := resp.GetWouldDelete()
	var markedCount int64
	for _, c := range candidates {
		if _, err := p.metaClient.MarkManifestRetentionPending(ctx, &metadatav1.MarkManifestRetentionPendingRequest{
			TenantId:   tenantID,
			ManifestId: c.GetManifestId(),
		}); err != nil {
			// Single failure is non-fatal — the next sweep will retry. Idempotent
			// MarkPending guarantees we don't double-stamp surviving rows.
			slog.WarnContext(ctx, "retention: mark pending failed",
				"run_id", run.RunID, "manifest_id", c.GetManifestId(), "error", err)
			continue
		}
		markedCount++
	}

	slog.InfoContext(ctx, "retention: soft-delete sweep complete",
		"run_id", run.RunID, "repo_id", repoID,
		"marked", markedCount, "would_delete_total", resp.GetTotalCount())

	// FE-API-041: only emit retention.applied when we actually stamped at
	// least one manifest. A zero-mark sweep already shows up as a gc_runs
	// row + retention.evaluated event, no need for a second "nothing
	// happened" signal.
	if markedCount > 0 {
		p.publishRetentionApplied(ctx, run, markedCount, resp.GetTotalCount())
	}

	return p.finalize(ctx, run.RunID, markedCount, 0, 0, "")
}

// ─── RunRetentionGrace (finaliser sweep) ────────────────────────────────────

// RunRetentionGrace deletes manifests whose retention_pending_delete_at is
// older than the configured grace window. Called from the dispatcher when
// it drains a queued retention_grace row, AND from the cross-tenant grace
// ticker that enqueues a fresh retention_grace row every
// RETENTION_GRACE_INTERVAL_HOURS.
//
// Tenant scoping: when run.TenantID is uuid.Nil the sweep is cross-tenant
// (used by the ticker). When set it's per-tenant (used by an operator-
// triggered grace sweep). Per-repo grace is not currently exposed — the
// soft-delete and finaliser are deliberately separated to keep the
// idempotent re-run semantics simple.
func (p *PersistedRunner) RunRetentionGrace(ctx context.Context, run *repository.GCRun) error {
	if p.metaClient == nil {
		return p.fail(ctx, run.RunID, "metadata client not configured")
	}

	tenantID := ""
	if run.TenantID != uuid.Nil {
		tenantID = run.TenantID.String()
	}

	resp, err := p.metaClient.ListPendingDeleteManifests(ctx, &metadatav1.ListPendingDeleteManifestsRequest{
		TenantId:         tenantID,
		GraceWindowSecs:  int64(p.retention.GraceWindow / time.Second),
		Limit:            int32(p.retention.MaxGraceCandidatesPerRun),
	})
	if err != nil {
		return p.fail(ctx, run.RunID, fmt.Sprintf("list pending: %v", err))
	}

	pending := resp.GetManifests()
	// Aggregate the would-delete totals for the evaluated event before the
	// finaliser actually touches anything — same "subscriber sees it about
	// to happen" contract as the soft-delete sweep.
	var wouldDeleteBytes int64
	for _, m := range pending {
		wouldDeleteBytes += m.GetSizeBytes()
	}
	p.publishRetentionEvaluated(ctx, run, "retention_grace",
		int64(len(pending)), wouldDeleteBytes, nil)

	var (
		deleted    int64
		blobs      int64
		bytesFreed int64
	)
	for _, m := range pending {
		if _, err := p.metaClient.DeleteManifest(ctx, &metadatav1.DeleteManifestRequest{
			TenantId: m.GetTenantId(),
			RepoId:   m.GetRepositoryId(),
			Digest:   m.GetDigest(),
		}); err != nil {
			// Don't abort the sweep — failed deletes will resurface next tick.
			slog.WarnContext(ctx, "retention_grace: delete manifest failed",
				"run_id", run.RunID, "manifest_id", m.GetManifestId(),
				"digest", m.GetDigest(), "error", err)
			continue
		}
		deleted++
		bytesFreed += m.GetSizeBytes()
		// blobs_freed is set by the orphan-blob sweep that runs separately —
		// we leave the counter at zero here so the dashboard column doesn't
		// double-count. Documented in the GCRun comment.
	}

	slog.InfoContext(ctx, "retention_grace: finaliser sweep complete",
		"run_id", run.RunID, "deleted", deleted, "bytes_freed", bytesFreed,
		"considered", len(pending))

	// FE-API-041: only emit retention.grace_completed when something was
	// actually hard-deleted. Mirrors the retention.applied gate.
	if deleted > 0 {
		p.publishRetentionGraceCompleted(ctx, run, deleted, blobs, bytesFreed)
	}

	return p.finalize(ctx, run.RunID, deleted, blobs, bytesFreed, "")
}

// ─── Event publishers (FE-API-041) ──────────────────────────────────────────
//
// All three helpers share the same nil-safe contract: when p.pub is nil
// the helper logs at debug + returns so a dev install without a broker
// still drains queued retention runs cleanly. On publish error we log at
// warn + return — the gc_runs row already recorded the run's outcome so
// the event is a best-effort observability signal, not the source of
// truth. This matches the collector's publishStarted/publishCompleted
// behaviour upstream and keeps the executor focused on the metadata
// state machine.

// publishRetentionEvaluated emits retention.evaluated at the START of a
// sweep with the would-delete totals. previewUntil is non-nil only when
// the policy is in its FE-API-038 preview window — subscribers can use
// the timestamp to render "would delete after <date>" rather than a hard
// "deleted X manifests".
//
// Cross-tenant grace sweeps (TenantID == uuid.Nil) are intentionally NOT
// published: the webhook subscription model is per-tenant, every
// audit_events row carries a tenant_id, and an event with TenantID=""
// has nowhere to route. Surfaced 2026-06-25 as a `services/webhook`
// consumer error: `invalid tenant_id in event …: invalid UUID length: 0`
// retrying forever until the redeliver cap. We skip the publish at
// source rather than relying on every consumer to handle empty
// tenant_id; the gc_runs row + slog still capture the observability.
func (p *PersistedRunner) publishRetentionEvaluated(
	ctx context.Context,
	run *repository.GCRun,
	mode string,
	wouldDeleteCount, wouldDeleteBytes int64,
	previewUntil *time.Time,
) {
	if p.pub == nil {
		slog.DebugContext(ctx, "retention.evaluated: publisher not configured, skipping",
			"run_id", run.RunID)
		return
	}
	if run.TenantID == uuid.Nil {
		slog.DebugContext(ctx, "retention.evaluated: cross-tenant run, skipping publish",
			"run_id", run.RunID)
		return
	}
	payload, _ := json.Marshal(events.RetentionEvaluatedPayload{
		RunID:              run.RunID.String(),
		TenantID:           tenantString(run.TenantID),
		RepositoryID:       repoString(run.RepoID),
		Mode:               mode,
		EvaluatedAt:        time.Now().UTC(),
		WouldDeleteCount:   wouldDeleteCount,
		WouldDeleteBytes:   wouldDeleteBytes,
		PolicyPreviewUntil: previewUntil,
		TriggeredBy:        run.TriggeredBy,
	})
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingRetentionEvaluated,
		TenantID:   tenantString(run.TenantID),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := p.pub.Publish(ctx, events.RoutingRetentionEvaluated, evt); err != nil {
		slog.WarnContext(ctx, "retention.evaluated publish failed",
			"run_id", run.RunID, "err", err)
	}
}

// publishRetentionApplied emits retention.applied after a successful
// soft-delete sweep. Called only when markedCount > 0 — see RunRetention.
func (p *PersistedRunner) publishRetentionApplied(
	ctx context.Context,
	run *repository.GCRun,
	marked, considered int64,
) {
	if p.pub == nil {
		slog.DebugContext(ctx, "retention.applied: publisher not configured, skipping",
			"run_id", run.RunID)
		return
	}
	// See publishRetentionEvaluated above — cross-tenant runs have no
	// webhook target so we skip rather than publishing tenant_id="".
	if run.TenantID == uuid.Nil {
		slog.DebugContext(ctx, "retention.applied: cross-tenant run, skipping publish",
			"run_id", run.RunID)
		return
	}
	payload, _ := json.Marshal(events.RetentionAppliedPayload{
		RunID:               run.RunID.String(),
		TenantID:            tenantString(run.TenantID),
		RepositoryID:        repoString(run.RepoID),
		CompletedAt:         time.Now().UTC(),
		ManifestsMarked:     marked,
		ManifestsConsidered: considered,
		TriggeredBy:         run.TriggeredBy,
	})
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingRetentionApplied,
		TenantID:   tenantString(run.TenantID),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := p.pub.Publish(ctx, events.RoutingRetentionApplied, evt); err != nil {
		slog.WarnContext(ctx, "retention.applied publish failed",
			"run_id", run.RunID, "err", err)
	}
}

// publishRetentionGraceCompleted emits retention.grace_completed after a
// finaliser sweep that hard-deleted at least one manifest. blobs is
// always zero today (the orphan-blob sweep owns that counter) but the
// field is on the wire so a future change can populate it without
// breaking subscribers.
func (p *PersistedRunner) publishRetentionGraceCompleted(
	ctx context.Context,
	run *repository.GCRun,
	deleted, blobs, bytesFreed int64,
) {
	if p.pub == nil {
		slog.DebugContext(ctx, "retention.grace_completed: publisher not configured, skipping",
			"run_id", run.RunID)
		return
	}
	// See publishRetentionEvaluated above — cross-tenant runs have no
	// webhook target so we skip rather than publishing tenant_id="".
	if run.TenantID == uuid.Nil {
		slog.DebugContext(ctx, "retention.grace_completed: cross-tenant run, skipping publish",
			"run_id", run.RunID)
		return
	}
	payload, _ := json.Marshal(events.RetentionGraceCompletedPayload{
		RunID:            run.RunID.String(),
		TenantID:         tenantString(run.TenantID),
		CompletedAt:      time.Now().UTC(),
		ManifestsDeleted: deleted,
		BlobsFreed:       blobs,
		BytesFreed:       bytesFreed,
		TriggeredBy:      run.TriggeredBy,
	})
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingRetentionGraceCompleted,
		TenantID:   tenantString(run.TenantID),
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := p.pub.Publish(ctx, events.RoutingRetentionGraceCompleted, evt); err != nil {
		slog.WarnContext(ctx, "retention.grace_completed publish failed",
			"run_id", run.RunID, "err", err)
	}
}

// policyToCandidate copies the persisted policy back into the candidate
// shape EvaluateRetention expects. Used by the preview-window branch where
// we still want a dry-run to populate the retention.evaluated payload.
func policyToCandidate(policy *metadatav1.RetentionPolicy) *metadatav1.RetentionPolicyCandidate {
	return &metadatav1.RetentionPolicyCandidate{
		Enabled:              policy.GetEnabled(),
		Rules:                policy.GetRules(),
		ProtectedTagPatterns: policy.GetProtectedTagPatterns(),
	}
}

// tenantString returns "" for uuid.Nil (cross-tenant runs) so consumers
// can distinguish a platform-wide grace sweep from a tenant-scoped one.
func tenantString(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// repoString returns "" for uuid.Nil (tenant-wide or cross-tenant runs).
func repoString(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// IsRetentionMode reports whether a gc_runs mode value corresponds to a
// retention executor mode. Centralising the check keeps the dispatcher
// switch and the handler-side validation in sync.
func IsRetentionMode(mode string) bool {
	return mode == "retention" || mode == "retention_grace"
}

// asNonNilErr returns the error message body for FailRun. Centralised so a
// future change can swap nil-handling without touching every call site.
func asNonNilErr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return err.Error()
}

var _ = asNonNilErr // reserved for FE-API-041's failure-path enrichment.
