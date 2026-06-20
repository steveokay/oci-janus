package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

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
