package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeRepo is a test double for auditRepo.
type fakeRepo struct {
	buildHistory []*repository.BuildHistoryRow
	buildErr     error
	pullCount    int64
	pullErr      error
}

func (f *fakeRepo) GetBuildHistory(_ context.Context, _ uuid.UUID, _, _ string, _ int) ([]*repository.BuildHistoryRow, error) {
	return f.buildHistory, f.buildErr
}

func (f *fakeRepo) CountPulls(_ context.Context, _ uuid.UUID, _ time.Time) (int64, error) {
	return f.pullCount, f.pullErr
}

func newHandler(repo auditRepo) *GRPCHandler {
	return &GRPCHandler{repo: repo}
}

// ---------------------------------------------------------------------------
// GetBuildHistory
// ---------------------------------------------------------------------------

func TestGetBuildHistory_validRequest_returnsBuilds(t *testing.T) {
	tenantID := uuid.New()
	repoID := uuid.New().String()
	now := time.Now().UTC().Truncate(time.Second)

	fake := &fakeRepo{
		buildHistory: []*repository.BuildHistoryRow{
			{ID: uuid.New(), ActorID: "user1", Outcome: "success", OccurredAt: now},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: tenantID.String(),
		RepoId:   repoID,
		Tag:      "v1.0",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotal() != 1 {
		t.Errorf("expected Total=1, got %d", resp.GetTotal())
	}
	if len(resp.GetBuilds()) != 1 {
		t.Fatalf("expected 1 build, got %d", len(resp.GetBuilds()))
	}
	b := resp.GetBuilds()[0]
	if b.GetStatus() != "success" {
		t.Errorf("expected status 'success', got %q", b.GetStatus())
	}
	if b.GetTriggeredBy() != "user1" {
		t.Errorf("expected triggered_by 'user1', got %q", b.GetTriggeredBy())
	}
}

func TestGetBuildHistory_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: "not-a-uuid",
		RepoId:   "some-repo-id",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetBuildHistory_emptyRepoID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: uuid.New().String(),
		RepoId:   "",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetBuildHistory_repoError_returnsInternal(t *testing.T) {
	fake := &fakeRepo{buildErr: errors.New("db offline")}
	h := newHandler(fake)

	_, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: uuid.New().String(),
		RepoId:   "some-repo-id",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

func TestGetBuildHistory_emptyResult_returnsZeroTotal(t *testing.T) {
	h := newHandler(&fakeRepo{buildHistory: nil})

	resp, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: uuid.New().String(),
		RepoId:   "some-repo-id",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotal() != 0 {
		t.Errorf("expected Total=0, got %d", resp.GetTotal())
	}
}

func TestGetBuildHistory_failedOutcome_mapsToBuildStatusFailed(t *testing.T) {
	fake := &fakeRepo{
		buildHistory: []*repository.BuildHistoryRow{
			{ID: uuid.New(), Outcome: "failure", OccurredAt: time.Now()},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetBuildHistory(context.Background(), &auditv1.GetBuildHistoryRequest{
		TenantId: uuid.New().String(),
		RepoId:   "some-id",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetBuilds()[0].GetStatus() != "failed" {
		t.Errorf("expected status 'failed', got %q", resp.GetBuilds()[0].GetStatus())
	}
}

// ---------------------------------------------------------------------------
// GetDailyPullCount
// ---------------------------------------------------------------------------

func TestGetDailyPullCount_validRequest_returnsCount(t *testing.T) {
	fake := &fakeRepo{pullCount: 42}
	h := newHandler(fake)

	resp, err := h.GetDailyPullCount(context.Background(), &auditv1.GetDailyPullCountRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetCount() != 42 {
		t.Errorf("expected count=42, got %d", resp.GetCount())
	}
}

func TestGetDailyPullCount_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetDailyPullCount(context.Background(), &auditv1.GetDailyPullCountRequest{
		TenantId: "bad-id",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetDailyPullCount_repoError_returnsInternal(t *testing.T) {
	fake := &fakeRepo{pullErr: errors.New("timeout")}
	h := newHandler(fake)

	_, err := h.GetDailyPullCount(context.Background(), &auditv1.GetDailyPullCountRequest{
		TenantId: uuid.New().String(),
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

func TestGetDailyPullCount_zeroCount_isValid(t *testing.T) {
	h := newHandler(&fakeRepo{pullCount: 0})

	resp, err := h.GetDailyPullCount(context.Background(), &auditv1.GetDailyPullCountRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetCount() != 0 {
		t.Errorf("expected 0, got %d", resp.GetCount())
	}
}
