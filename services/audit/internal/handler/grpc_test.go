package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// fakeRepo is a test double for auditRepo.
type fakeRepo struct {
	buildHistory []*repository.BuildHistoryRow
	buildErr     error
	pullCount    int64
	pullErr      error

	// activity holds the rows returned to GetRepoActivity calls. To keep the
	// tests synchronous + deterministic we don't bother emulating the keyset
	// pagination ourselves — callers either set this slice short enough to fit
	// in the limit, or the activityResponder hook below to drive paginated
	// behaviour explicitly.
	activity    []*repository.RepoActivityRow
	activityErr error

	// activityCalls captures the parameters of each GetRepoActivity call so
	// tests can assert that handler-level filtering / clamping was applied.
	activityCalls []activityCall

	// activityResponder, when non-nil, overrides the default behaviour above
	// so a test can return different page contents depending on the cursor.
	activityResponder func(call activityCall) ([]*repository.RepoActivityRow, error)

	// notifications holds the rows returned to GetNotifications calls. As
	// with activity above the tests either set this slice short enough to
	// fit in the limit, or override behaviour with notificationsResponder.
	notifications     []*repository.NotificationRow
	notificationsErr  error
	notificationCalls []notificationCall

	// notificationsResponder lets a test return different pages per cursor.
	notificationsResponder func(call notificationCall) ([]*repository.NotificationRow, error)

	// analytics holds the rows GetAnalytics returns. Tests either set this
	// slice directly or override via analyticsResponder for per-call shaping.
	analytics    []*repository.AnalyticsBucketRow
	analyticsErr error

	// analyticsCalls captures parameters of every GetAnalytics invocation so
	// handler tests can assert clamping and scope routing.
	analyticsCalls []analyticsCall

	// analyticsResponder, when non-nil, overrides the default analytics
	// behaviour so a test can return different rows depending on the
	// scope / action / range.
	analyticsResponder func(call analyticsCall) ([]*repository.AnalyticsBucketRow, error)

	// lastPush state for the GetLastTenantPush RPC (FE-API-028). lastPushFor
	// keys by tenant so tests can assert per-tenant isolation in one fake.
	lastPushFor map[uuid.UUID]time.Time
	lastPushErr error

	// FUT-019 Phase 3 — email channel fake state (see grpc_email_test.go for
	// the method implementations). emailCfg is the row Get returns; Upsert
	// captures the sealed config + refreshes emailCfg so a follow-up Get
	// reflects the write. testResult* capture the last UpdateEmailTestResult.
	emailCfg        *repository.EmailTransportConfig
	emailCfgErr     error
	upsertedEmail   *repository.EmailTransportConfig
	testResultSet   bool
	testResultOK    bool
	testResultErr   string
	emailDeliveries []*repository.EmailDelivery
	lastListLimit   int

	// FUT-019 Webhook channel fake state (see grpc_notification_webhook_test.go
	// for the method implementations). webhookCfg is the row Get returns; Upsert
	// stores into it so the Put→reload roundtrip reflects the write.
	webhookCfg *repository.NotificationWebhookConfig
}

// analyticsCall captures the parameters passed to one GetAnalytics call.
type analyticsCall struct {
	tenantID   uuid.UUID
	scope      repository.AnalyticsScope
	action     string
	rangeStart time.Time
	rangeEnd   time.Time
	bucketSecs int64
}

// notificationCall captures the parameters passed to one GetNotifications call.
type notificationCall struct {
	tenantID   uuid.UUID
	since      time.Time
	cursorTime time.Time
	cursorID   uuid.UUID
	eventTypes []string
	limit      int
}

// activityCall captures the parameters passed to one GetRepoActivity invocation.
type activityCall struct {
	tenantID       uuid.UUID
	repositoryName string
	since          time.Time
	cursorTime     time.Time
	cursorID       uuid.UUID
	eventTypes     []string
	limit          int
}

func (f *fakeRepo) GetBuildHistory(_ context.Context, _ uuid.UUID, _, _ string, _ int) ([]*repository.BuildHistoryRow, error) {
	return f.buildHistory, f.buildErr
}

func (f *fakeRepo) CountPulls(_ context.Context, _ uuid.UUID, _ time.Time) (int64, error) {
	return f.pullCount, f.pullErr
}

func (f *fakeRepo) GetRepoActivity(
	_ context.Context,
	tenantID uuid.UUID,
	repositoryName string,
	since time.Time,
	cursorTime time.Time,
	cursorID uuid.UUID,
	eventTypes []string,
	limit int,
) ([]*repository.RepoActivityRow, error) {
	call := activityCall{
		tenantID:       tenantID,
		repositoryName: repositoryName,
		since:          since,
		cursorTime:     cursorTime,
		cursorID:       cursorID,
		eventTypes:     append([]string(nil), eventTypes...),
		limit:          limit,
	}
	f.activityCalls = append(f.activityCalls, call)
	if f.activityResponder != nil {
		return f.activityResponder(call)
	}
	return f.activity, f.activityErr
}

func (f *fakeRepo) GetNotifications(
	_ context.Context,
	tenantID uuid.UUID,
	since time.Time,
	cursorTime time.Time,
	cursorID uuid.UUID,
	eventTypes []string,
	limit int,
) ([]*repository.NotificationRow, error) {
	call := notificationCall{
		tenantID:   tenantID,
		since:      since,
		cursorTime: cursorTime,
		cursorID:   cursorID,
		eventTypes: append([]string(nil), eventTypes...),
		limit:      limit,
	}
	f.notificationCalls = append(f.notificationCalls, call)
	if f.notificationsResponder != nil {
		return f.notificationsResponder(call)
	}
	return f.notifications, f.notificationsErr
}

func (f *fakeRepo) GetAnalytics(
	_ context.Context,
	tenantID uuid.UUID,
	scope repository.AnalyticsScope,
	action string,
	rangeStart time.Time,
	rangeEnd time.Time,
	bucketSecs int64,
) ([]*repository.AnalyticsBucketRow, error) {
	call := analyticsCall{
		tenantID:   tenantID,
		scope:      scope,
		action:     action,
		rangeStart: rangeStart,
		rangeEnd:   rangeEnd,
		bucketSecs: bucketSecs,
	}
	f.analyticsCalls = append(f.analyticsCalls, call)
	if f.analyticsResponder != nil {
		return f.analyticsResponder(call)
	}
	return f.analytics, f.analyticsErr
}

func (f *fakeRepo) GetLastTenantPush(_ context.Context, tenantID uuid.UUID) (time.Time, bool, error) {
	if f.lastPushErr != nil {
		return time.Time{}, false, f.lastPushErr
	}
	t, ok := f.lastPushFor[tenantID]
	return t, ok, nil
}

// Audit-log streaming to SIEM (futures.md Tier 1 #4). Stub the new
// AuditExportConfig CRUD on the fake — the existing test suites don't
// exercise these paths; the dedicated audit_export_test.go covers the
// happy paths + a live-stack smoke covers cross-service integration.
func (f *fakeRepo) GetAuditExportConfig(_ context.Context, _ uuid.UUID) (*repository.AuditExportConfig, error) {
	return nil, repository.ErrExportConfigNotFound
}

func (f *fakeRepo) UpsertAuditExportConfig(_ context.Context, cfg *repository.AuditExportConfig) (*repository.AuditExportConfig, error) {
	return cfg, nil
}

func (f *fakeRepo) DeleteAuditExportConfig(_ context.Context, _ uuid.UUID) error {
	return nil
}

// FUT-019 Phase 2 — fake notification-preference methods. Tests that
// exercise the new RPCs build dedicated fixtures; the existing repo
// tests use these no-op stubs to satisfy the auditRepo interface.
func (f *fakeRepo) GetUserPreferences(_ context.Context, _ uuid.UUID) ([]*repository.NotificationPreference, error) {
	return nil, nil
}
func (f *fakeRepo) UpsertUserPreference(_ context.Context, _ repository.NotificationPreference) error {
	return nil
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

// ---------------------------------------------------------------------------
// GetRepoActivity
// ---------------------------------------------------------------------------

// activityMetadata is a small helper that builds the JSON the repository scan
// produces for an audit row, mirroring services/audit/internal/eventconsumer
// which wraps the raw payload under "raw".
func activityMetadata(t *testing.T, raw map[string]any) []byte {
	t.Helper()
	rawBytes, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw payload: %v", err)
	}
	wrapped := map[string]any{
		"event_id": uuid.New().String(),
		"raw":      json.RawMessage(rawBytes),
	}
	b, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("marshal wrapped metadata: %v", err)
	}
	return b
}

func TestGetRepoActivity_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       "not-a-uuid",
		RepositoryName: "myorg/myrepo",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetRepoActivity_emptyRepoName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetRepoActivity_unknownEventType_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		EventTypes:     []string{"push.image", "shenanigans"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for unknown event_type, got %v", err)
	}
}

func TestGetRepoActivity_emptyResult_returnsNoNextPage(t *testing.T) {
	h := newHandler(&fakeRepo{activity: nil})

	resp, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEvents()) != 0 {
		t.Errorf("expected 0 events, got %d", len(resp.GetEvents()))
	}
	if resp.GetNextPageToken() != "" {
		t.Errorf("expected empty next_page_token, got %q", resp.GetNextPageToken())
	}
}

func TestGetRepoActivity_mixedTypes_projectsExpectedFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	pushID := uuid.New()
	scanID := uuid.New()
	fake := &fakeRepo{
		activity: []*repository.RepoActivityRow{
			{
				ID:         pushID,
				ActorID:    "user-1",
				Action:     "push.image",
				Outcome:    "success",
				OccurredAt: now,
				Metadata: activityMetadata(t, map[string]any{
					"repository_name": "myorg/myrepo",
					"tag":             "v1.2.3",
					"manifest_digest": "sha256:aaa",
					"pushed_by":       "alice",
				}),
			},
			{
				ID:         scanID,
				ActorID:    "system",
				Action:     "scan.completed",
				Outcome:    "failure",
				OccurredAt: now.Add(-time.Minute),
				Metadata: activityMetadata(t, map[string]any{
					"repository_name": "myorg/myrepo",
					"manifest_digest": "sha256:bbb",
					"scanner_name":    "trivy",
				}),
			},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetEvents()) != 2 {
		t.Fatalf("expected 2 events, got %d", len(resp.GetEvents()))
	}
	push := resp.GetEvents()[0]
	if push.GetEventType() != "push.image" {
		t.Errorf("expected push.image, got %q", push.GetEventType())
	}
	if push.GetTag() != "v1.2.3" {
		t.Errorf("expected tag v1.2.3, got %q", push.GetTag())
	}
	if push.GetDigest() != "sha256:aaa" {
		t.Errorf("expected digest sha256:aaa, got %q", push.GetDigest())
	}
	if push.GetActorUsername() != "alice" {
		t.Errorf("expected actor_username alice, got %q", push.GetActorUsername())
	}
	if push.GetSummary() == "" {
		t.Errorf("expected non-empty summary for push.image")
	}
	scan := resp.GetEvents()[1]
	if scan.GetOutcome() != "failure" {
		t.Errorf("expected outcome failure, got %q", scan.GetOutcome())
	}
	if scan.GetSummary() == "" {
		t.Errorf("expected non-empty summary for scan.completed")
	}
}

func TestGetRepoActivity_emptyEventTypes_appliesDefaultAllowlist(t *testing.T) {
	fake := &fakeRepo{activity: nil}
	h := newHandler(fake)

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.activityCalls) != 1 {
		t.Fatalf("expected 1 repo call, got %d", len(fake.activityCalls))
	}
	got := fake.activityCalls[0].eventTypes
	// The handler must NOT have passed an empty slice through to the repo —
	// the repo treats empty as "no rows", which would surprise the caller.
	if len(got) == 0 {
		t.Fatal("expected handler to substitute the default event_types allowlist")
	}
	// And `webhook.queued` (an internal event) must not have leaked into the
	// default — it's noise for operators.
	for _, et := range got {
		if et == "webhook.queued" || et == "scan.queued" || et == "store.queued" {
			t.Errorf("internal event type %q leaked into default allowlist", et)
		}
	}
}

func TestGetRepoActivity_callerSuppliedTypes_passedThrough(t *testing.T) {
	fake := &fakeRepo{activity: nil}
	h := newHandler(fake)

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		EventTypes:     []string{"push.image"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fake.activityCalls[0].eventTypes; len(got) != 1 || got[0] != "push.image" {
		t.Errorf("expected event_types=[push.image], got %v", got)
	}
}

func TestGetRepoActivity_limitClampedToMax(t *testing.T) {
	fake := &fakeRepo{activity: nil}
	h := newHandler(fake)

	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		Limit:          9999,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// limit + 1 row is fetched for has-next-page detection. Effective max is 200.
	if got := fake.activityCalls[0].limit; got != 201 {
		t.Errorf("expected repo limit=201 (200 cap +1 lookahead), got %d", got)
	}
}

func TestGetRepoActivity_sinceClampedTo90Days(t *testing.T) {
	fake := &fakeRepo{activity: nil}
	h := newHandler(fake)

	veryOld := time.Now().Add(-2 * 365 * 24 * time.Hour)
	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		Since:          timestamppb.New(veryOld),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	since := fake.activityCalls[0].since
	if time.Since(since) > 91*24*time.Hour {
		t.Errorf("expected since clamped to ~90 days, got %v ago", time.Since(since))
	}
}

func TestGetRepoActivity_pagination_emitsTokenAndAcceptsIt(t *testing.T) {
	// Seed three rows; with limit=2 the handler should fetch 3, return 2, and
	// emit a token whose decoded position matches the second row.
	now := time.Now().UTC().Truncate(time.Microsecond)
	rowA := &repository.RepoActivityRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now,
		Metadata:   activityMetadata(t, map[string]any{"repository_name": "myorg/myrepo", "tag": "a"}),
	}
	rowB := &repository.RepoActivityRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now.Add(-time.Minute),
		Metadata:   activityMetadata(t, map[string]any{"repository_name": "myorg/myrepo", "tag": "b"}),
	}
	rowC := &repository.RepoActivityRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now.Add(-2 * time.Minute),
		Metadata:   activityMetadata(t, map[string]any{"repository_name": "myorg/myrepo", "tag": "c"}),
	}

	fake := &fakeRepo{
		activityResponder: func(call activityCall) ([]*repository.RepoActivityRow, error) {
			// First page: no cursor → return [A, B, C]; handler keeps 2.
			if call.cursorTime.IsZero() {
				return []*repository.RepoActivityRow{rowA, rowB, rowC}, nil
			}
			// Second page: handler should have set cursor to rowB's (time, id).
			if !call.cursorTime.Equal(rowB.OccurredAt) || call.cursorID != rowB.ID {
				return nil, errors.New("wrong cursor on page 2")
			}
			return []*repository.RepoActivityRow{rowC}, nil
		},
	}
	h := newHandler(fake)

	first, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		Limit:          2,
	})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	if len(first.GetEvents()) != 2 {
		t.Fatalf("page 1: expected 2 events, got %d", len(first.GetEvents()))
	}
	if first.GetNextPageToken() == "" {
		t.Fatal("page 1: expected non-empty next_page_token")
	}

	second, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		Limit:          2,
		PageToken:      first.GetNextPageToken(),
	})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	if len(second.GetEvents()) != 1 {
		t.Fatalf("page 2: expected 1 event, got %d", len(second.GetEvents()))
	}
	if second.GetNextPageToken() != "" {
		t.Errorf("page 2: expected empty next_page_token, got %q", second.GetNextPageToken())
	}
}

func TestGetRepoActivity_invalidPageToken_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
		PageToken:      "***not-base64***",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetRepoActivity_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{activityErr: errors.New("db offline")})
	_, err := h.GetRepoActivity(context.Background(), &auditv1.GetRepoActivityRequest{
		TenantId:       uuid.New().String(),
		RepositoryName: "myorg/myrepo",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetNotifications (FE-API-008)
// ---------------------------------------------------------------------------

func TestGetNotifications_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: "not-a-uuid",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetNotifications_unknownEventType_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId:   uuid.New().String(),
		EventTypes: []string{"push.image", "shenanigans"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for unknown event_type, got %v", err)
	}
}

func TestGetNotifications_emptyResult_returnsZeroUnread(t *testing.T) {
	fake := &fakeRepo{notifications: nil}
	h := newHandler(fake)

	resp, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNotifications()) != 0 {
		t.Errorf("expected 0 notifications, got %d", len(resp.GetNotifications()))
	}
	if resp.GetUnreadCount() != 0 {
		t.Errorf("expected unread_count=0, got %d", resp.GetUnreadCount())
	}
	if resp.GetNextPageToken() != "" {
		t.Errorf("expected empty next_page_token, got %q", resp.GetNextPageToken())
	}
}

func TestGetNotifications_emptyEventTypes_appliesDefaultAllowlist(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)

	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.notificationCalls) != 1 {
		t.Fatalf("expected 1 repo call, got %d", len(fake.notificationCalls))
	}
	got := fake.notificationCalls[0].eventTypes
	if len(got) == 0 {
		t.Fatal("expected handler to substitute the default event_types allowlist")
	}
	// The default set must include the FE-API-008 additions (push.failed,
	// webhook.delivery_failed) and must NOT leak internal queue noise.
	want := map[string]bool{
		"push.image":              true,
		"push.failed":             true,
		"scan.completed":          true,
		"webhook.delivery_failed": true,
	}
	have := map[string]bool{}
	for _, et := range got {
		have[et] = true
		if et == "webhook.queued" || et == "scan.queued" || et == "store.queued" {
			t.Errorf("internal event type %q leaked into default allowlist", et)
		}
	}
	for et := range want {
		if !have[et] {
			t.Errorf("default allowlist missing %q (got %v)", et, got)
		}
	}
}

func TestGetNotifications_callerSuppliedTypes_passedThrough(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)

	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId:   uuid.New().String(),
		EventTypes: []string{"push.failed", "webhook.delivery_failed"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := fake.notificationCalls[0].eventTypes
	if len(got) != 2 || got[0] != "push.failed" || got[1] != "webhook.delivery_failed" {
		t.Errorf("event_types: got %v, want [push.failed webhook.delivery_failed]", got)
	}
}

func TestGetNotifications_sinceClampedTo90Days(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)

	veryOld := time.Now().Add(-2 * 365 * 24 * time.Hour)
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
		Since:    timestamppb.New(veryOld),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	since := fake.notificationCalls[0].since
	if time.Since(since) > 91*24*time.Hour {
		t.Errorf("expected since clamped to ~90 days, got %v ago", time.Since(since))
	}
}

func TestGetNotifications_limitClampedToMax(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
		Limit:    9999,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// limit+1 row is fetched for has-next-page detection. Effective cap is 200.
	if got := fake.notificationCalls[0].limit; got != 201 {
		t.Errorf("expected repo limit=201 (200 cap +1 lookahead), got %d", got)
	}
}

func TestGetNotifications_invalidPageToken_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId:  uuid.New().String(),
		PageToken: "***not-base64***",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetNotifications_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{notificationsErr: errors.New("db offline")})
	_, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

// notificationMetadata mirrors activityMetadata above — wraps a raw payload in
// the {"event_id": ..., "raw": payload} envelope that the eventconsumer
// writes for every audit row.
func notificationMetadata(t *testing.T, raw map[string]any) []byte {
	t.Helper()
	rawBytes, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw payload: %v", err)
	}
	wrapped := map[string]any{
		"event_id": uuid.New().String(),
		"raw":      json.RawMessage(rawBytes),
	}
	b, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("marshal wrapped metadata: %v", err)
	}
	return b
}

func TestGetNotifications_rendersPushTitleAndLink(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	fake := &fakeRepo{
		notifications: []*repository.NotificationRow{
			{
				ID:         uuid.New(),
				Action:     "push.image",
				Outcome:    "success",
				OccurredAt: now,
				Metadata: notificationMetadata(t, map[string]any{
					"repository_name": "acme/registry",
					"tag":             "3.20",
					"manifest_digest": "sha256:aaa",
					"pushed_by":       "alice",
				}),
			},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNotifications()) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(resp.GetNotifications()))
	}
	n := resp.GetNotifications()[0]
	if n.GetTitle() != "Push completed" {
		t.Errorf("Title: got %q, want %q", n.GetTitle(), "Push completed")
	}
	if n.GetSummary() != "acme/registry:3.20 pushed" {
		t.Errorf("Summary: got %q, want %q", n.GetSummary(), "acme/registry:3.20 pushed")
	}
	if n.GetLink() != "/repositories/acme/registry/tags/3.20" {
		t.Errorf("Link: got %q, want %q", n.GetLink(), "/repositories/acme/registry/tags/3.20")
	}
	if n.GetActorUsername() != "alice" {
		t.Errorf("ActorUsername: got %q, want alice", n.GetActorUsername())
	}
	if n.GetMetadata()["repo"] != "acme/registry" {
		t.Errorf("Metadata[repo]: got %q, want acme/registry", n.GetMetadata()["repo"])
	}
	if n.GetMetadata()["tag"] != "3.20" {
		t.Errorf("Metadata[tag]: got %q, want 3.20", n.GetMetadata()["tag"])
	}
	if resp.GetUnreadCount() != 1 {
		t.Errorf("UnreadCount: got %d, want 1", resp.GetUnreadCount())
	}
}

func TestGetNotifications_rendersWebhookFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	fake := &fakeRepo{
		notifications: []*repository.NotificationRow{
			{
				ID:         uuid.New(),
				Action:     "webhook.delivery_failed",
				Outcome:    "failure",
				OccurredAt: now,
				Metadata: notificationMetadata(t, map[string]any{
					"webhook_id":  "wh-123",
					"url":         "https://example.com/hook",
					"status_code": 503,
				}),
			},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId:   uuid.New().String(),
		EventTypes: []string{"webhook.delivery_failed"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNotifications()) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(resp.GetNotifications()))
	}
	n := resp.GetNotifications()[0]
	if n.GetTitle() != "Webhook failed" {
		t.Errorf("Title: got %q, want %q", n.GetTitle(), "Webhook failed")
	}
	if n.GetSummary() != "https://example.com/hook — 503" {
		t.Errorf("Summary: got %q, want %q", n.GetSummary(), "https://example.com/hook — 503")
	}
	if n.GetLink() != "/webhooks/wh-123" {
		t.Errorf("Link: got %q, want %q", n.GetLink(), "/webhooks/wh-123")
	}
	// Webhook-delivery payloads have no repo so the metadata bag must not
	// carry an empty repo key.
	if _, present := n.GetMetadata()["repo"]; present {
		t.Errorf("Metadata leaked empty repo key")
	}
	if n.GetMetadata()["url"] != "https://example.com/hook" {
		t.Errorf("Metadata[url] missing or wrong")
	}
}

func TestGetNotifications_pagination_emitsTokenAndAcceptsIt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rowA := &repository.NotificationRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now,
		Metadata: notificationMetadata(t, map[string]any{
			"repository_name": "org/repo", "tag": "a",
		}),
	}
	rowB := &repository.NotificationRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now.Add(-time.Minute),
		Metadata: notificationMetadata(t, map[string]any{
			"repository_name": "org/repo", "tag": "b",
		}),
	}
	rowC := &repository.NotificationRow{
		ID: uuid.New(), Action: "push.image", Outcome: "success",
		OccurredAt: now.Add(-2 * time.Minute),
		Metadata: notificationMetadata(t, map[string]any{
			"repository_name": "org/repo", "tag": "c",
		}),
	}

	fake := &fakeRepo{
		notificationsResponder: func(call notificationCall) ([]*repository.NotificationRow, error) {
			if call.cursorTime.IsZero() {
				// Page 1: return [A, B, C]; handler keeps 2 and emits a token.
				return []*repository.NotificationRow{rowA, rowB, rowC}, nil
			}
			// Page 2: cursor should be rowB's (occurred_at, id).
			if !call.cursorTime.Equal(rowB.OccurredAt) || call.cursorID != rowB.ID {
				return nil, errors.New("wrong cursor on page 2")
			}
			return []*repository.NotificationRow{rowC}, nil
		},
	}
	h := newHandler(fake)

	first, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId: uuid.New().String(),
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	if len(first.GetNotifications()) != 2 {
		t.Fatalf("page 1: expected 2 notifications, got %d", len(first.GetNotifications()))
	}
	if first.GetNextPageToken() == "" {
		t.Fatal("page 1: expected non-empty next_page_token")
	}

	second, err := h.GetNotifications(context.Background(), &auditv1.GetNotificationsRequest{
		TenantId:  uuid.New().String(),
		Limit:     2,
		PageToken: first.GetNextPageToken(),
	})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	if len(second.GetNotifications()) != 1 {
		t.Fatalf("page 2: expected 1 notification, got %d", len(second.GetNotifications()))
	}
	if second.GetNextPageToken() != "" {
		t.Errorf("page 2: expected empty next_page_token, got %q", second.GetNextPageToken())
	}
}

// ---------------------------------------------------------------------------
// GetLastTenantPush (FE-API-028)
// ---------------------------------------------------------------------------

// TestGetLastTenantPush_noEvents_returnsNilTimestamp covers the freshly created
// tenant case: no push.image rows yet, so the wire timestamp must be nil. The
// management layer translates that into `last_push_at: null` in the JSON.
func TestGetLastTenantPush_noEvents_returnsNilTimestamp(t *testing.T) {
	h := newHandler(&fakeRepo{})
	resp, err := h.GetLastTenantPush(context.Background(), &auditv1.GetLastTenantPushRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetLastPushAt() != nil {
		t.Errorf("expected nil LastPushAt, got %v", resp.GetLastPushAt())
	}
}

// TestGetLastTenantPush_withEvents_returnsLatestTimestamp covers the happy path
// — the most recent push.image timestamp surfaces on the wire.
func TestGetLastTenantPush_withEvents_returnsLatestTimestamp(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	older := now.Add(-7 * 24 * time.Hour)
	fake := &fakeRepo{
		lastPushFor: map[uuid.UUID]time.Time{
			tenantA: now,
			tenantB: older,
		},
	}
	h := newHandler(fake)

	resp, err := h.GetLastTenantPush(context.Background(), &auditv1.GetLastTenantPushRequest{
		TenantId: tenantA.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts := resp.GetLastPushAt(); ts == nil {
		t.Fatal("expected non-nil LastPushAt")
	} else if !ts.AsTime().Equal(now) {
		t.Errorf("LastPushAt: got %v, want %v", ts.AsTime(), now)
	}

	// And verify the per-tenant isolation: tenantB sees the older timestamp.
	resp2, err := h.GetLastTenantPush(context.Background(), &auditv1.GetLastTenantPushRequest{
		TenantId: tenantB.String(),
	})
	if err != nil {
		t.Fatalf("tenant B: unexpected error: %v", err)
	}
	if !resp2.GetLastPushAt().AsTime().Equal(older) {
		t.Errorf("tenant B: got %v, want %v", resp2.GetLastPushAt().AsTime(), older)
	}
}

// TestGetLastTenantPush_invalidTenantID_returnsInvalidArgument verifies the
// shape check fires before any DB call.
func TestGetLastTenantPush_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetLastTenantPush(context.Background(), &auditv1.GetLastTenantPushRequest{
		TenantId: "not-a-uuid",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

// TestGetLastTenantPush_repoError_returnsInternal verifies that an underlying
// DB error gets mapped to a non-InvalidArgument status (the exact code is
// chosen by MapDBError; we only check it's not InvalidArgument so a future
// reclassification doesn't break this case).
func TestGetLastTenantPush_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{lastPushErr: errors.New("db down")})
	_, err := h.GetLastTenantPush(context.Background(), &auditv1.GetLastTenantPushRequest{
		TenantId: uuid.New().String(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) == codes.InvalidArgument {
		t.Errorf("did not expect InvalidArgument for repo error, got %v", err)
	}
}
