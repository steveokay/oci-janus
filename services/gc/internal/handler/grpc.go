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
	ListRuns(ctx context.Context, limit int, pageToken string) ([]*repository.GCRun, string, error)
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

	rows, next, err := h.repo.ListRuns(ctx, limit, req.GetPageToken())
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
