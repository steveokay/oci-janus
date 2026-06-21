// Tests for the FE-API-040 retention RPCs on the GCService gRPC handler.
// Same hand-written fakeRepo as the rest of this package — focused on the
// new TriggerRetentionRun + GetRetentionRunStatus paths.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

const validUserUUID = "11111111-1111-1111-1111-111111111111"

// ── TriggerRetentionRun ──────────────────────────────────────────────────────

// TestTriggerRetention_happyPath_returnsQueued verifies the success path:
// the RPC inserts a queued retention row and returns {run_id, "queued"}.
func TestTriggerRetention_happyPath_returnsQueued(t *testing.T) {
	ch := make(chan uuid.UUID, 1)
	h := New(&fakeRepo{}, ch, time.Hour)
	resp, err := h.TriggerRetentionRun(context.Background(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    uuid.NewString(),
		RepoId:      uuid.NewString(),
		TriggeredBy: validUserUUID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetStatus() != "queued" {
		t.Errorf("status: got %q, want queued", resp.GetStatus())
	}
	if resp.GetRunId() == "" {
		t.Error("run_id must be populated")
	}
	// Channel should have received the dispatcher hint.
	select {
	case <-ch:
	default:
		t.Error("expected dispatcher hint to be sent")
	}
}

// TestTriggerRetention_missingRepoID_rejected.
func TestTriggerRetention_missingRepoID_rejected(t *testing.T) {
	ch := make(chan uuid.UUID, 1)
	h := New(&fakeRepo{}, ch, time.Hour)
	_, err := h.TriggerRetentionRun(context.Background(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    uuid.NewString(),
		TriggeredBy: validUserUUID,
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestTriggerRetention_invalidRepoID_rejected verifies the UUID parse fires.
func TestTriggerRetention_invalidRepoID_rejected(t *testing.T) {
	ch := make(chan uuid.UUID, 1)
	h := New(&fakeRepo{}, ch, time.Hour)
	_, err := h.TriggerRetentionRun(context.Background(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    uuid.NewString(),
		RepoId:      "not-a-uuid",
		TriggeredBy: validUserUUID,
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestTriggerRetention_repoFailsToCreate_returns500.
func TestTriggerRetention_repoFailsToCreate_returns500(t *testing.T) {
	ch := make(chan uuid.UUID, 1)
	h := New(&fakeRepo{retentionCreateErr: errors.New("db down")}, ch, time.Hour)
	_, err := h.TriggerRetentionRun(context.Background(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    uuid.NewString(),
		RepoId:      uuid.NewString(),
		TriggeredBy: validUserUUID,
	})
	requireCode(t, err, codes.Internal)
}

// TestTriggerRetention_disabledDispatcher_failsPrecondition verifies the
// "RunNow disabled" branch fires when runRequests is nil.
func TestTriggerRetention_disabledDispatcher_failsPrecondition(t *testing.T) {
	h := New(&fakeRepo{}, nil, time.Hour)
	_, err := h.TriggerRetentionRun(context.Background(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    uuid.NewString(),
		RepoId:      uuid.NewString(),
		TriggeredBy: validUserUUID,
	})
	requireCode(t, err, codes.FailedPrecondition)
}

// ── GetRetentionRunStatus ────────────────────────────────────────────────────

// TestGetRetentionRunStatus_happyPath_returnsMode verifies the run row's
// counters land on the mode-specific fields.
func TestGetRetentionRunStatus_happyPath_returnsMode(t *testing.T) {
	runID := uuid.New()
	repoID := uuid.New()
	tenantID := uuid.New()
	completed := time.Now().UTC()
	repo := &fakeRepo{
		getRunByIDResult: &repository.GCRun{
			RunID:            runID,
			TenantID:         tenantID,
			RepoID:           repoID,
			Mode:             "retention",
			Status:           "succeeded",
			RequestedAt:      completed.Add(-2 * time.Minute),
			StartedAt:        completed.Add(-1 * time.Minute),
			CompletedAt:      completed,
			ManifestsDeleted: 42, // counted as "marked" because mode=retention
			TriggeredBy:      validUserUUID,
		},
	}
	h := New(repo, nil, time.Hour)

	got, err := h.GetRetentionRunStatus(context.Background(), &gcv1.GetRetentionRunStatusRequest{
		RunId:    runID.String(),
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.GetManifestsMarked() != 42 {
		t.Errorf("manifests_marked: got %d, want 42 (mode=retention)", got.GetManifestsMarked())
	}
	if got.GetManifestsDeleted() != 0 {
		t.Errorf("manifests_deleted should be 0 for mode=retention, got %d", got.GetManifestsDeleted())
	}
	if got.GetRepoId() != repoID.String() {
		t.Errorf("repo_id: got %q, want %q", got.GetRepoId(), repoID.String())
	}
}

// TestGetRetentionRunStatus_gracePopulatesDeletedCounters verifies the
// mode-specific mapping for retention_grace.
func TestGetRetentionRunStatus_gracePopulatesDeletedCounters(t *testing.T) {
	repo := &fakeRepo{
		getRunByIDResult: &repository.GCRun{
			RunID:            uuid.New(),
			Mode:             "retention_grace",
			Status:           "succeeded",
			ManifestsDeleted: 5,
			BytesFreed:       1 << 30,
		},
	}
	h := New(repo, nil, time.Hour)

	got, err := h.GetRetentionRunStatus(context.Background(), &gcv1.GetRetentionRunStatusRequest{
		RunId: repo.getRunByIDResult.RunID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.GetManifestsDeleted() != 5 {
		t.Errorf("manifests_deleted: got %d, want 5 (mode=retention_grace)", got.GetManifestsDeleted())
	}
	if got.GetBytesFreed() != 1<<30 {
		t.Errorf("bytes_freed: got %d, want 1GiB", got.GetBytesFreed())
	}
}

// TestGetRetentionRunStatus_notFound_returns404.
func TestGetRetentionRunStatus_notFound_returns404(t *testing.T) {
	h := New(&fakeRepo{getRunByIDErr: repository.ErrNotFound}, nil, time.Hour)
	_, err := h.GetRetentionRunStatus(context.Background(), &gcv1.GetRetentionRunStatusRequest{
		RunId: uuid.NewString(),
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetRetentionRunStatus_invalidRunID_returns400.
func TestGetRetentionRunStatus_invalidRunID_returns400(t *testing.T) {
	h := New(&fakeRepo{}, nil, time.Hour)
	_, err := h.GetRetentionRunStatus(context.Background(), &gcv1.GetRetentionRunStatusRequest{
		RunId: "bad",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// requireCode asserts that err carries the expected gRPC status code. We use
// the standard grpcstatus.FromError rather than a hand-rolled interface
// assertion because *status.Error wraps the actual implementation type.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	st, ok := grpcstatus.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != want {
		t.Errorf("code: got %v, want %v", st.Code(), want)
	}
}

// suppress unused import warning if errors is later removed; keep available
// for future test additions.
var _ = errors.New
