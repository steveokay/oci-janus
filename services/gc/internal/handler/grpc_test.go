// Package handler_test exercises the GCService gRPC handler against a
// hand-written fake Repository. No Postgres / gRPC server / pgxpool
// involvement — every behaviour the handler implements is observable
// purely through the interface contract.
package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
)

// fakeRepo is a minimal in-memory implementation of the Repository
// interface used by the handler. Tests can pre-seed `latest` for
// GetLatest and `runs` for ListRuns, or override the create hook to
// simulate failures.
type fakeRepo struct {
	latest         *repository.GCRun
	latestErr      error
	runs           []*repository.GCRun
	listErr        error
	listNext       string
	createErr      error
	createOverride func(mode string, tenantID uuid.UUID, triggeredBy string) (*repository.GCRun, error)
	// captured arguments for assertion.
	lastListLimit int
	lastListToken string
	// REM-013 gap 2 — new ListRuns filter params.
	lastListRepoID uuid.UUID
	lastListModes  []string
	// S-MAINT-1 F2 — search params from the same ListRunsFilters bundle.
	lastListTriggeredBy string
	lastListDateFrom    *time.Time
	lastListDateTo      *time.Time
	lastCreateMode      string
	lastCreateBy        string

	// FE-API-040 retention executor fakes.
	retentionCreateErr      error
	retentionCreateOverride func(mode string, tenantID, repoID uuid.UUID, triggeredBy string) (*repository.GCRun, error)
	lastRetentionMode       string
	lastRetentionRepo       uuid.UUID
	lastRetentionTenant     uuid.UUID
	lastRetentionTriggered  string

	getRunByIDResult *repository.GCRun
	getRunByIDErr    error
	lastGetRunID     uuid.UUID
	lastGetTenantID  uuid.UUID

	// REM-013 gap 3 — retention savings aggregate fake.
	savings           repository.RetentionSavings
	savingsErr        error
	lastSavingsTenant uuid.UUID
}

func (f *fakeRepo) CreateRun(_ context.Context, mode string, tenantID uuid.UUID, triggeredBy string) (*repository.GCRun, error) {
	f.lastCreateMode = mode
	f.lastCreateBy = triggeredBy
	if f.createOverride != nil {
		return f.createOverride(mode, tenantID, triggeredBy)
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &repository.GCRun{
		RunID:       uuid.New(),
		Mode:        mode,
		Status:      "queued",
		RequestedAt: time.Now().UTC(),
		TriggeredBy: triggeredBy,
	}, nil
}

func (f *fakeRepo) GetLatest(_ context.Context) (*repository.GCRun, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	if f.latest == nil {
		return nil, repository.ErrNotFound
	}
	return f.latest, nil
}

func (f *fakeRepo) ListRuns(_ context.Context, limit int, pageToken string, filters repository.ListRunsFilters) ([]*repository.GCRun, string, error) {
	// REM-013 gap 2 + S-MAINT-1 F2 — record the filter params so per-repo /
	// mode / search tests can assert the handler plumbed them through.
	// Existing callers that don't care still see the same `runs` slice
	// back; the new fields live on the same fakeRepo struct.
	f.lastListLimit = limit
	f.lastListToken = pageToken
	f.lastListRepoID = filters.RepoID
	f.lastListModes = filters.Modes
	f.lastListTriggeredBy = filters.TriggeredBy
	f.lastListDateFrom = filters.DateFrom
	f.lastListDateTo = filters.DateTo
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	return f.runs, f.listNext, nil
}

// FE-API-040 — retention executor fake methods.
func (f *fakeRepo) CreateRetentionRun(_ context.Context, mode string, tenantID, repoID uuid.UUID, triggeredBy string) (*repository.GCRun, error) {
	f.lastRetentionMode = mode
	f.lastRetentionRepo = repoID
	f.lastRetentionTenant = tenantID
	f.lastRetentionTriggered = triggeredBy
	if f.retentionCreateOverride != nil {
		return f.retentionCreateOverride(mode, tenantID, repoID, triggeredBy)
	}
	if f.retentionCreateErr != nil {
		return nil, f.retentionCreateErr
	}
	return &repository.GCRun{
		RunID:       uuid.New(),
		TenantID:    tenantID,
		RepoID:      repoID,
		Mode:        mode,
		Status:      "queued",
		RequestedAt: time.Now().UTC(),
		TriggeredBy: triggeredBy,
	}, nil
}

func (f *fakeRepo) GetRunByID(_ context.Context, runID, tenantID uuid.UUID) (*repository.GCRun, error) {
	f.lastGetRunID = runID
	f.lastGetTenantID = tenantID
	if f.getRunByIDErr != nil {
		return nil, f.getRunByIDErr
	}
	if f.getRunByIDResult == nil {
		return nil, repository.ErrNotFound
	}
	return f.getRunByIDResult, nil
}

// REM-013 gap 3 — retention savings aggregate fake.
func (f *fakeRepo) GetTenantRetentionSavings(_ context.Context, tenantID uuid.UUID) (repository.RetentionSavings, error) {
	f.lastSavingsTenant = tenantID
	if f.savingsErr != nil {
		return repository.RetentionSavings{}, f.savingsErr
	}
	return f.savings, nil
}

// ─── GetStatus ──────────────────────────────────────────────────────────────

// TestGetStatus_NoRuns_returnsEmptyStatus verifies the fresh-service
// state: GetLatest returns ErrNotFound, the handler must return a
// zero-valued GCStatus and never NPE on missing timestamps.
func TestGetStatus_NoRuns_returnsEmptyStatus(t *testing.T) {
	h := New(&fakeRepo{}, nil, 24*time.Hour, 7)
	resp, err := h.GetStatus(context.Background(), &gcv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLastRunId() != "" || resp.GetLastRunStatus() != "" {
		t.Errorf("expected empty last_run_*, got %+v", resp)
	}
	if resp.GetNextScheduledAt() == nil {
		t.Error("next_scheduled_at should be populated from `now` when no runs exist")
	}
	// The configured grace window is reported even before the first run.
	if resp.GetRetentionGraceDays() != 7 {
		t.Errorf("retention_grace_days: got %d, want 7", resp.GetRetentionGraceDays())
	}
}

// TestGetStatus_WithCompletedRun_populatesAllFields verifies every
// last_run_* field on GCStatus carries through from the repository
// row, and next_scheduled_at projects from the completion time.
func TestGetStatus_WithCompletedRun_populatesAllFields(t *testing.T) {
	completed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	started := completed.Add(-2 * time.Minute)
	repo := &fakeRepo{
		latest: &repository.GCRun{
			RunID:            uuid.New(),
			Mode:             "full",
			Status:           "succeeded",
			RequestedAt:      started,
			StartedAt:        started,
			CompletedAt:      completed,
			DurationMS:       120_000,
			BlobsFreed:       42,
			ManifestsDeleted: 7,
			BytesFreed:       1024 * 1024,
			TriggeredBy:      "cron",
		},
	}
	h := New(repo, nil, 24*time.Hour, 14)
	resp, err := h.GetStatus(context.Background(), &gcv1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetRetentionGraceDays() != 14 {
		t.Errorf("retention_grace_days: got %d, want 14", resp.GetRetentionGraceDays())
	}
	if resp.GetLastRunStatus() != "succeeded" {
		t.Errorf("status: got %q", resp.GetLastRunStatus())
	}
	if resp.GetLastRunBlobsFreed() != 42 || resp.GetLastRunManifestsDeleted() != 7 {
		t.Errorf("counters: got %d/%d", resp.GetLastRunBlobsFreed(), resp.GetLastRunManifestsDeleted())
	}
	if resp.GetLastRunCompletedAt() == nil {
		t.Fatal("completed_at: got nil")
	}
	if !resp.GetLastRunCompletedAt().AsTime().Equal(completed) {
		t.Errorf("completed_at: got %v, want %v", resp.GetLastRunCompletedAt().AsTime(), completed)
	}
	next := resp.GetNextScheduledAt()
	if next == nil || !next.AsTime().Equal(completed.Add(24*time.Hour)) {
		t.Errorf("next_scheduled_at: got %v, want %v", next.AsTime(), completed.Add(24*time.Hour))
	}
}

// TestGetStatus_RepoError_returnsInternal verifies a non-NotFound
// repository error surfaces as codes.Internal.
func TestGetStatus_RepoError_returnsInternal(t *testing.T) {
	h := New(&fakeRepo{latestErr: errors.New("boom")}, nil, 0, 0)
	_, err := h.GetStatus(context.Background(), &gcv1.GetStatusRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("code: got %v, want Internal", st.Code())
	}
}

// ─── RunNow ─────────────────────────────────────────────────────────────────

// TestRunNow_HappyPath_returnsQueuedRunID verifies the create path:
// the row is INSERTed, the response carries `queued`, and the
// dispatcher channel receives a hint.
func TestRunNow_HappyPath_returnsQueuedRunID(t *testing.T) {
	repo := &fakeRepo{}
	dispatch := make(chan uuid.UUID, 1)
	h := New(repo, dispatch, time.Hour, 0)

	caller := uuid.New().String()
	resp, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "full",
		TriggeredBy: caller,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetStatus() != "queued" {
		t.Errorf("status: got %q, want queued", resp.GetStatus())
	}
	if resp.GetRunId() == "" {
		t.Error("run_id should not be empty")
	}
	if repo.lastCreateMode != "full" || repo.lastCreateBy != caller {
		t.Errorf("create args: mode=%q triggered_by=%q", repo.lastCreateMode, repo.lastCreateBy)
	}
	select {
	case got := <-dispatch:
		// runID generated inside CreateRun — we don't compare the
		// value, just that a hint arrived.
		_ = got
	default:
		t.Error("expected a dispatch hint on the channel")
	}
}

// TestRunNow_InvalidMode_returnsInvalidArgument verifies the mode
// allowlist fires before any repository call.
func TestRunNow_InvalidMode_returnsInvalidArgument(t *testing.T) {
	repo := &fakeRepo{}
	dispatch := make(chan uuid.UUID, 1)
	h := New(repo, dispatch, 0, 0)

	_, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "obliterate",
		TriggeredBy: uuid.NewString(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
	if repo.lastCreateMode != "" {
		t.Error("repo should not have been called for an invalid mode")
	}
}

// TestRunNow_MalformedTriggeredBy_returnsInvalidArgument verifies the
// UUID validation gate.
func TestRunNow_MalformedTriggeredBy_returnsInvalidArgument(t *testing.T) {
	h := New(&fakeRepo{}, make(chan uuid.UUID, 1), 0, 0)
	_, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "dry-run",
		TriggeredBy: "not-a-uuid",
	})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestRunNow_NoDispatcher_returnsFailedPrecondition verifies the
// handler refuses RunNow when the dispatcher channel is nil — better
// to surface a clear status than silently insert a row no worker
// will ever drain.
func TestRunNow_NoDispatcher_returnsFailedPrecondition(t *testing.T) {
	h := New(&fakeRepo{}, nil, 0, 0)
	_, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "full",
		TriggeredBy: uuid.NewString(),
	})
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", st.Code())
	}
}

// TestRunNow_DispatcherFull_stillSucceeds verifies the channel send
// is best-effort: a full buffer doesn't block the RPC because the
// queued row in the DB is already the authoritative source.
func TestRunNow_DispatcherFull_stillSucceeds(t *testing.T) {
	dispatch := make(chan uuid.UUID, 1)
	// Pre-fill the channel so the next send would block.
	dispatch <- uuid.New()
	h := New(&fakeRepo{}, dispatch, 0, 0)
	resp, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "manifests",
		TriggeredBy: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetStatus() != "queued" {
		t.Errorf("status: got %q, want queued", resp.GetStatus())
	}
}

// TestRunNow_CreateFails_returnsInternal verifies a repository error
// surfaces as Internal so the BFF can return 500.
func TestRunNow_CreateFails_returnsInternal(t *testing.T) {
	h := New(&fakeRepo{createErr: errors.New("db down")}, make(chan uuid.UUID, 1), 0, 0)
	_, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{
		Mode:        "blobs",
		TriggeredBy: uuid.NewString(),
	})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("code: got %v, want Internal", st.Code())
	}
}

// ─── ListRuns ───────────────────────────────────────────────────────────────

// TestListRuns_Happy_returnsAllRows verifies a non-paginated call
// returns the seeded rows in the order the repository hands them
// over.
func TestListRuns_Happy_returnsAllRows(t *testing.T) {
	rows := []*repository.GCRun{
		{RunID: uuid.New(), Mode: "full", Status: "succeeded", RequestedAt: time.Now()},
		{RunID: uuid.New(), Mode: "dry-run", Status: "running", RequestedAt: time.Now()},
	}
	h := New(&fakeRepo{runs: rows}, nil, 0, 0)
	resp, err := h.ListRuns(context.Background(), &gcv1.ListRunsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := len(resp.GetRuns()), len(rows); got != want {
		t.Errorf("rows: got %d, want %d", got, want)
	}
	if resp.GetRuns()[0].GetMode() != "full" {
		t.Errorf("first row mode: got %q, want full", resp.GetRuns()[0].GetMode())
	}
}

// TestListRuns_DefaultLimit_appliesFifty verifies that page_size = 0
// is rewritten to 50 before reaching the repository.
func TestListRuns_DefaultLimit_appliesFifty(t *testing.T) {
	repo := &fakeRepo{}
	h := New(repo, nil, 0, 0)
	_, _ = h.ListRuns(context.Background(), &gcv1.ListRunsRequest{})
	if repo.lastListLimit != 50 {
		t.Errorf("default limit: got %d, want 50", repo.lastListLimit)
	}
}

// TestListRuns_LimitClampedTo200 verifies callers can't request more
// rows than the hard cap.
func TestListRuns_LimitClampedTo200(t *testing.T) {
	repo := &fakeRepo{}
	h := New(repo, nil, 0, 0)
	_, _ = h.ListRuns(context.Background(), &gcv1.ListRunsRequest{PageSize: 5000})
	if repo.lastListLimit != 200 {
		t.Errorf("clamp: got %d, want 200", repo.lastListLimit)
	}
}

// TestListRuns_NextPageToken_propagates verifies the repository's
// cursor is returned to the caller verbatim.
func TestListRuns_NextPageToken_propagates(t *testing.T) {
	h := New(&fakeRepo{listNext: "cursor-abc"}, nil, 0, 0)
	resp, err := h.ListRuns(context.Background(), &gcv1.ListRunsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetNextPageToken() != "cursor-abc" {
		t.Errorf("next_page_token: got %q, want cursor-abc", resp.GetNextPageToken())
	}
}

// TestListRuns_BadPageToken_returnsInvalidArgument verifies the
// repository's wrapped error maps onto the gRPC INVALID_ARGUMENT code.
func TestListRuns_BadPageToken_returnsInvalidArgument(t *testing.T) {
	h := New(&fakeRepo{listErr: fmt.Errorf("invalid page_token: %w", errors.New("bad base64"))}, nil, 0, 0)
	_, err := h.ListRuns(context.Background(), &gcv1.ListRunsRequest{PageToken: "garbage"})
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestListRuns_GenericRepoError_returnsInternal verifies a non-cursor
// repository error becomes codes.Internal.
func TestListRuns_GenericRepoError_returnsInternal(t *testing.T) {
	h := New(&fakeRepo{listErr: errors.New("db down")}, nil, 0, 0)
	_, err := h.ListRuns(context.Background(), &gcv1.ListRunsRequest{})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("code: got %v, want Internal", st.Code())
	}
}

// TestNoRepository_AllRPCsReturnFailedPrecondition verifies the safety
// net: a handler with no repository wired is reachable (the server
// registered it) but every method short-circuits cleanly.
func TestNoRepository_AllRPCsReturnFailedPrecondition(t *testing.T) {
	h := New(nil, nil, 0, 0)
	if _, err := h.GetStatus(context.Background(), &gcv1.GetStatusRequest{}); err == nil ||
		statusCode(err) != codes.FailedPrecondition {
		t.Errorf("GetStatus: got %v, want FailedPrecondition", err)
	}
	if _, err := h.ListRuns(context.Background(), &gcv1.ListRunsRequest{}); err == nil ||
		statusCode(err) != codes.FailedPrecondition {
		t.Errorf("ListRuns: got %v, want FailedPrecondition", err)
	}
	if _, err := h.RunNow(context.Background(), &gcv1.RunNowRequest{Mode: "full", TriggeredBy: uuid.NewString()}); err == nil ||
		statusCode(err) != codes.FailedPrecondition {
		t.Errorf("RunNow: got %v, want FailedPrecondition", err)
	}
}

func statusCode(err error) codes.Code {
	st, _ := status.FromError(err)
	return st.Code()
}

// ─── GetTenantRetentionSavings (REM-013 gap 3) ────────────────────────────────

// TestGetTenantRetentionSavings_mapsAggregate verifies the handler forwards the
// tenant id to the repo and maps every aggregate field (including a non-nil
// last_run_at) onto the wire message.
func TestGetTenantRetentionSavings_mapsAggregate(t *testing.T) {
	tenant := uuid.New()
	last := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	repo := &fakeRepo{savings: repository.RetentionSavings{
		ReclaimedBytes:   4096,
		ManifestsDeleted: 8,
		RunCount:         2,
		LastRunAt:        &last,
	}}
	h := New(repo, nil, 0, 0)

	resp, err := h.GetTenantRetentionSavings(context.Background(), &gcv1.GetTenantRetentionSavingsRequest{
		TenantId: tenant.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastSavingsTenant != tenant {
		t.Errorf("tenant not forwarded: got %v, want %v", repo.lastSavingsTenant, tenant)
	}
	if resp.GetReclaimedBytes() != 4096 {
		t.Errorf("reclaimed_bytes: got %d, want 4096", resp.GetReclaimedBytes())
	}
	if resp.GetManifestsDeleted() != 8 {
		t.Errorf("manifests_deleted: got %d, want 8", resp.GetManifestsDeleted())
	}
	if resp.GetRunCount() != 2 {
		t.Errorf("run_count: got %d, want 2", resp.GetRunCount())
	}
	if resp.GetTenantId() != tenant.String() {
		t.Errorf("tenant_id echo: got %q, want %q", resp.GetTenantId(), tenant.String())
	}
	if resp.GetLastRunAt() == nil || !resp.GetLastRunAt().AsTime().Equal(last) {
		t.Errorf("last_run_at: got %v, want %v", resp.GetLastRunAt(), last)
	}
}

// TestGetTenantRetentionSavings_noRuns_returnsZero verifies the empty-state:
// a tenant with no completed retention runs yields all-zero counters and a nil
// last_run_at, with no error.
func TestGetTenantRetentionSavings_noRuns_returnsZero(t *testing.T) {
	h := New(&fakeRepo{}, nil, 0, 0)
	resp, err := h.GetTenantRetentionSavings(context.Background(), &gcv1.GetTenantRetentionSavingsRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetReclaimedBytes() != 0 || resp.GetRunCount() != 0 || resp.GetLastRunAt() != nil {
		t.Errorf("expected zero savings, got %+v", resp)
	}
}

// TestGetTenantRetentionSavings_badTenant_returnsInvalidArgument verifies the
// tenant-id validation path (empty + non-UUID).
func TestGetTenantRetentionSavings_badTenant_returnsInvalidArgument(t *testing.T) {
	h := New(&fakeRepo{}, nil, 0, 0)
	for _, tid := range []string{"", "not-a-uuid"} {
		_, err := h.GetTenantRetentionSavings(context.Background(), &gcv1.GetTenantRetentionSavingsRequest{TenantId: tid})
		if statusCode(err) != codes.InvalidArgument {
			t.Errorf("tenant_id %q: got %v, want InvalidArgument", tid, statusCode(err))
		}
	}
}

// TestGetTenantRetentionSavings_repoErr_returnsInternal verifies a repository
// failure surfaces as codes.Internal, not a panic.
func TestGetTenantRetentionSavings_repoErr_returnsInternal(t *testing.T) {
	h := New(&fakeRepo{savingsErr: errors.New("boom")}, nil, 0, 0)
	_, err := h.GetTenantRetentionSavings(context.Background(), &gcv1.GetTenantRetentionSavingsRequest{
		TenantId: uuid.New().String(),
	})
	if statusCode(err) != codes.Internal {
		t.Errorf("got %v, want Internal", statusCode(err))
	}
}
