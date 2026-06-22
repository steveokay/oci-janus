// Package handler implements the GCService gRPC server (FE-API-032).
//
// Three RPCs:
//
//   - GetStatus  — `last_run_*` snapshot + best-effort next_scheduled_at.
//   - RunNow     — INSERTs a queued row and signals the dispatcher
//                  channel; never blocks on the sweep itself.
//   - ListRuns   — paginated history with base64url keyset cursor.
//
// The repository handle and the dispatch channel are both optional on
// the handler struct. When either is nil the relevant RPC returns
// codes.FailedPrecondition rather than panicking; production wiring
// always provides both, but the unit tests for ListRuns / GetStatus
// can skip the dispatch channel entirely.
package handler

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// Repository is the narrow contract the gRPC handler needs. Defining
// it here (rather than importing *repository.Repository directly) lets
// the handler tests inject a hand-written fake without standing up
// pgxpool.
type Repository interface {
	CreateRun(ctx context.Context, mode string, tenantID uuid.UUID, triggeredBy string) (*repository.GCRun, error)
	GetLatest(ctx context.Context) (*repository.GCRun, error)
	// REM-013 gap 2 — repoID == uuid.Nil disables the repo filter; modes
	// == nil or empty disables the mode filter. Both preserve the old
	// behaviour so the existing platform-admin /admin/gc/runs route keeps
	// working with no caller-side changes.
	ListRuns(ctx context.Context, limit int, pageToken string, filters repository.ListRunsFilters) ([]*repository.GCRun, string, error)
	// FE-API-040 — retention executor surface.
	CreateRetentionRun(ctx context.Context, mode string, tenantID, repoID uuid.UUID, triggeredBy string) (*repository.GCRun, error)
	GetRunByID(ctx context.Context, runID, tenantID uuid.UUID) (*repository.GCRun, error)
}

// validModes mirrors the allowlist from the GC_MODE config flag.
// Centralised here so the handler rejects unknown modes before they
// reach Postgres (where the gc_run_mode enum would bounce them anyway).
var validModes = map[string]bool{
	"dry-run":   true,
	"manifests": true,
	"blobs":     true,
	"full":      true,
}

// GRPCHandler implements gcv1.GCServiceServer.
type GRPCHandler struct {
	gcv1.UnimplementedGCServiceServer

	repo            Repository
	// runRequests carries run_ids freshly inserted by RunNow to the
	// dispatcher goroutine that consumes them between cron ticks. nil
	// means "RunNow disabled" — the handler still serves GetStatus /
	// ListRuns but rejects new sweeps with FailedPrecondition.
	runRequests chan<- uuid.UUID
	// scheduleInterval is the cron tick interval used to project a
	// best-effort next_scheduled_at into GCStatus. Zero means "unknown"
	// and the handler omits the field.
	scheduleInterval time.Duration
}

// New creates a GRPCHandler. Pass a nil runRequests to disable RunNow
// (useful for read-only unit tests).
func New(repo Repository, runRequests chan<- uuid.UUID, scheduleInterval time.Duration) *GRPCHandler {
	return &GRPCHandler{
		repo:             repo,
		runRequests:      runRequests,
		scheduleInterval: scheduleInterval,
	}
}

// ---------------------------------------------------------------------------
// GetStatus
// ---------------------------------------------------------------------------

// GetStatus returns the latest run summary plus a best-effort
// next_scheduled_at projection. When no rows exist yet every
// last_run_* field is zero/empty and the BFF renders the "no runs"
// state without any error path.
func (h *GRPCHandler) GetStatus(ctx context.Context, _ *gcv1.GetStatusRequest) (*gcv1.GCStatus, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc repository not configured")
	}
	rec, err := h.repo.GetLatest(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Empty status — no runs yet. Still populate
			// next_scheduled_at off `now` so the dashboard shows when
			// the first sweep is expected.
			return &gcv1.GCStatus{
				NextScheduledAt: h.projectedNextSchedule(time.Time{}),
			}, nil
		}
		return nil, status.Errorf(codes.Internal, "get latest run: %v", err)
	}

	out := &gcv1.GCStatus{
		LastRunId:               rec.RunID.String(),
		LastRunMode:             rec.Mode,
		LastRunStatus:           rec.Status,
		LastRunDurationMs:       rec.DurationMS,
		LastRunBlobsFreed:       rec.BlobsFreed,
		LastRunManifestsDeleted: rec.ManifestsDeleted,
		LastRunBytesFreed:       rec.BytesFreed,
		LastRunError:            rec.ErrorMessage,
		LastRunTriggeredBy:      rec.TriggeredBy,
	}
	if ts := tsFromOptional(rec.StartedAt); ts != nil {
		out.LastRunStartedAt = ts
	}
	if ts := tsFromOptional(rec.CompletedAt); ts != nil {
		out.LastRunCompletedAt = ts
	}
	out.NextScheduledAt = h.projectedNextSchedule(rec.CompletedAt)
	return out, nil
}

// projectedNextSchedule returns lastCompleted + scheduleInterval when
// both are known. Returns nil when no projection can be made (interval
// unknown or last run hasn't completed). This is best-effort — the
// real cron tick fires off the in-process ticker, not this field.
func (h *GRPCHandler) projectedNextSchedule(lastCompleted time.Time) *timestamppb.Timestamp {
	if h.scheduleInterval <= 0 {
		return nil
	}
	base := lastCompleted
	if base.IsZero() || base.Year() <= 1970 {
		// No completed run yet — project from now.
		base = time.Now().UTC()
	}
	return timestamppb.New(base.Add(h.scheduleInterval))
}

// ---------------------------------------------------------------------------
// RunNow
// ---------------------------------------------------------------------------

// RunNow inserts a queued row and notifies the dispatcher channel. The
// RPC returns immediately with {run_id, "queued"} — the dashboard
// polls GetStatus to observe progression.
//
// triggered_by is required and must be a UUID (the caller's user_id);
// the BFF enforces this via the platform-admin gate before forwarding.
func (h *GRPCHandler) RunNow(ctx context.Context, req *gcv1.RunNowRequest) (*gcv1.RunNowResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc repository not configured")
	}
	if h.runRequests == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc dispatcher not configured")
	}
	if !validModes[req.GetMode()] {
		return nil, status.Error(codes.InvalidArgument, "mode must be one of dry-run|manifests|blobs|full")
	}
	if req.GetTriggeredBy() == "" {
		return nil, status.Error(codes.InvalidArgument, "triggered_by is required")
	}
	if _, err := uuid.Parse(req.GetTriggeredBy()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "triggered_by must be a UUID")
	}

	rec, err := h.repo.CreateRun(ctx, req.GetMode(), uuid.Nil, req.GetTriggeredBy())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create run: %v", err)
	}

	// Best-effort notify; the dispatcher will pick the row up on its
	// next poll either way (ClaimNextQueued handles the persisted
	// queue). A blocked channel here means another sweep is already
	// being signalled and we can drop this hint — the row in `queued`
	// state is the source of truth.
	select {
	case h.runRequests <- rec.RunID:
	default:
	}

	return &gcv1.RunNowResponse{
		RunId:  rec.RunID.String(),
		Status: "queued",
	}, nil
}

// ---------------------------------------------------------------------------
// ListRuns
// ---------------------------------------------------------------------------

// ListRuns returns recent runs. Caller-supplied page_size clamps to
// [1, 200] with a default of 50. page_token is opaque base64url —
// invalid tokens map to INVALID_ARGUMENT.
func (h *GRPCHandler) ListRuns(ctx context.Context, req *gcv1.ListRunsRequest) (*gcv1.ListRunsResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc repository not configured")
	}
	limit := int(req.GetPageSize())
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}

	// REM-013 gap 2 — parse the new optional repo_id + modes filters from
	// the request. Empty repo_id maps to uuid.Nil; cardinality 0 modes
	// preserves the pre-REM-013 "all modes" semantics.
	var repoID uuid.UUID
	if rid := req.GetRepoId(); rid != "" {
		parsed, err := uuid.Parse(rid)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid repo_id")
		}
		repoID = parsed
	}

	// S-MAINT-1 F2 — optional triggered_by substring + date_from / date_to
	// window. Empty / unparseable date strings map to nil so the SQL
	// guard skips the predicate. Bad timestamps surface as a 400 so a
	// fat-fingered client gets a clear error instead of a silent
	// "nothing matched" empty page.
	var dateFrom, dateTo *time.Time
	if s := req.GetDateFrom(); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid date_from (RFC3339 required)")
		}
		dateFrom = &t
	}
	if s := req.GetDateTo(); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid date_to (RFC3339 required)")
		}
		dateTo = &t
	}

	rows, next, err := h.repo.ListRuns(ctx, limit, req.GetPageToken(), repository.ListRunsFilters{
		RepoID:      repoID,
		Modes:       req.GetModes(),
		TriggeredBy: req.GetTriggeredBy(),
		DateFrom:    dateFrom,
		DateTo:      dateTo,
	})
	if err != nil {
		// The repository layer surfaces a wrapped error for malformed
		// page_token; check the wrapped chain rather than a string
		// compare so the handler stays decoupled from the exact phrasing.
		if errors.Is(err, errInvalidPageToken{}) {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		// Otherwise treat it as a 500 unless the message literally
		// starts with "invalid page_token:" — the repository wraps
		// fmt.Errorf("invalid page_token: %w", err) so a string check
		// is the simplest substitute for a typed sentinel.
		if isInvalidPageTokenErr(err) {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		return nil, status.Errorf(codes.Internal, "list runs: %v", err)
	}

	out := &gcv1.ListRunsResponse{
		Runs:          make([]*gcv1.GCRun, 0, len(rows)),
		NextPageToken: next,
	}
	for _, r := range rows {
		out.Runs = append(out.Runs, runToProto(r))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// errInvalidPageToken is a sentinel reserved for a future repository
// refactor; today the repository returns a wrapped fmt.Errorf so we
// fall back to a string-prefix check below. Keeping the sentinel in
// place means upgrading the repository to errors.As is a one-line edit.
type errInvalidPageToken struct{}

func (errInvalidPageToken) Error() string { return "invalid page_token" }

// isInvalidPageTokenErr inspects a wrapped error chain for the
// distinctive `invalid page_token:` prefix the repository attaches. We
// avoid using strings.HasPrefix on the top-level error so wrapping
// (errors.Join etc) keeps working.
func isInvalidPageTokenErr(err error) bool {
	for err != nil {
		msg := err.Error()
		if len(msg) >= len("invalid page_token") && msg[:len("invalid page_token")] == "invalid page_token" {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// runToProto converts a repository row to its proto form. Nullable
// timestamps surface as nil so the JSON encoder emits null instead of
// a 1970 placeholder.
func runToProto(r *repository.GCRun) *gcv1.GCRun {
	out := &gcv1.GCRun{
		RunId:            r.RunID.String(),
		Mode:             r.Mode,
		Status:           r.Status,
		RequestedAt:      timestamppb.New(r.RequestedAt),
		DurationMs:       r.DurationMS,
		BlobsFreed:       r.BlobsFreed,
		ManifestsDeleted: r.ManifestsDeleted,
		BytesFreed:       r.BytesFreed,
		ErrorMessage:     r.ErrorMessage,
		TriggeredBy:      r.TriggeredBy,
	}
	if ts := tsFromOptional(r.StartedAt); ts != nil {
		out.StartedAt = ts
	}
	if ts := tsFromOptional(r.CompletedAt); ts != nil {
		out.CompletedAt = ts
	}
	return out
}

// tsFromOptional returns nil for the repository's sentinel zero/epoch
// timestamp so the wire form distinguishes "never happened" from
// "happened at epoch".
func tsFromOptional(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() || t.Year() <= 1970 {
		return nil
	}
	return timestamppb.New(t)
}

// ---------------------------------------------------------------------------
// FE-API-040 — retention executor RPCs
// ---------------------------------------------------------------------------

// TriggerRetentionRun queues a retention soft-delete sweep for one repo.
// The executor runs asynchronously; this RPC returns {run_id, status:"queued"}
// immediately. The BFF maps it onto POST
// /api/v1/repositories/{org}/{repo}/policies/retention/run, gated on repo
// admin / owner since retention eventually deletes manifests.
func (h *GRPCHandler) TriggerRetentionRun(ctx context.Context, req *gcv1.TriggerRetentionRunRequest) (*gcv1.TriggerRetentionRunResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc repository not configured")
	}
	if h.runRequests == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc dispatcher not configured")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "repo_id must be a UUID")
	}
	if req.GetTriggeredBy() == "" {
		return nil, status.Error(codes.InvalidArgument, "triggered_by is required")
	}
	if _, err := uuid.Parse(req.GetTriggeredBy()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "triggered_by must be a UUID")
	}

	rec, err := h.repo.CreateRetentionRun(ctx, "retention", tenantID, repoID, req.GetTriggeredBy())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create retention run: %v", err)
	}
	// Best-effort dispatcher notify. The persisted row is the source of
	// truth — a full channel just means the dispatcher will pick up the row
	// on its next poll instead.
	select {
	case h.runRequests <- rec.RunID:
	default:
	}
	return &gcv1.TriggerRetentionRunResponse{
		RunId:  rec.RunID.String(),
		Status: "queued",
	}, nil
}

// GetRetentionRunStatus reads back a single retention run row by id. Tenant
// scoping is enforced server-side via the GetRunByID query so a caller can
// only see their own tenant's runs.
func (h *GRPCHandler) GetRetentionRunStatus(ctx context.Context, req *gcv1.GetRetentionRunStatusRequest) (*gcv1.RetentionRunSummary, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "gc repository not configured")
	}
	if req.GetRunId() == "" {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	runID, err := uuid.Parse(req.GetRunId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "run_id must be a UUID")
	}
	var tenantID uuid.UUID
	if req.GetTenantId() != "" {
		tenantID, err = uuid.Parse(req.GetTenantId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
		}
	}

	rec, err := h.repo.GetRunByID(ctx, runID, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "run not found")
		}
		return nil, status.Errorf(codes.Internal, "get run: %v", err)
	}

	out := &gcv1.RetentionRunSummary{
		RunId:        rec.RunID.String(),
		RepoId:       rec.RepoID.String(),
		Mode:         rec.Mode,
		Status:       rec.Status,
		RequestedAt:  timestamppb.New(rec.RequestedAt),
		ErrorMessage: rec.ErrorMessage,
		TriggeredBy:  rec.TriggeredBy,
	}
	if ts := tsFromOptional(rec.StartedAt); ts != nil {
		out.StartedAt = ts
	}
	if ts := tsFromOptional(rec.CompletedAt); ts != nil {
		out.CompletedAt = ts
	}
	// Mode-specific counter mapping. The gc_runs row carries one
	// manifests_deleted column; the retention executor stores marks +
	// hard-deletes in the same place but the wire shape splits them for
	// dashboard clarity.
	switch rec.Mode {
	case "retention":
		out.ManifestsMarked = rec.ManifestsDeleted
	case "retention_grace":
		out.ManifestsDeleted = rec.ManifestsDeleted
		out.BlobsFreed = rec.BlobsFreed
		out.BytesFreed = rec.BytesFreed
	}
	// Zero RepoID on the wire surfaces the empty string rather than the
	// zero-UUID sentinel — the BFF and dashboard expect "" for cross-tenant
	// rows.
	if rec.RepoID == uuid.Nil {
		out.RepoId = ""
	}
	return out, nil
}
