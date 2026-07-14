// Package service — ActivityService tests (FE-API-048, Task 11).
//
// All tests use in-memory fakes and a hand-written fakeAuditClient — no real
// PostgreSQL, Redis, or gRPC server required. Tests run with plain `go test ./...`.
//
// Security coverage:
//   - T9:  TestActivity_CrossTenant404 — cross-tenant target → 404 (spec §5.3 step 2)
//   - M4a: TestActivity_NonAdminQueryingOther404 — non-admin querying another user → 404
//   - M4b: TestActivity_SelfQueryWorks — non-admin querying themselves → success
//   - M4c: TestActivity_AdminQueryingAnotherInTenant — admin querying other user → success
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── fakeUserRepoForActivity ───────────────────────────────────────────────────

// fakeUserRepoForActivity is a minimal in-memory fake that satisfies
// UserRepoForActivity. It stores users by ID so GetUserAnyKind can look
// them up without a real database.
type fakeUserRepoForActivity struct {
	// byID maps user ID → User record.
	byID map[uuid.UUID]*repository.User
}

func newFakeUserRepoForActivity() *fakeUserRepoForActivity {
	return &fakeUserRepoForActivity{byID: make(map[uuid.UUID]*repository.User)}
}

// seedHuman inserts a human user into the fake store for the given tenant and
// email, returning its ID. Callers can use the returned ID as CallerUserID or
// TargetUserID in ListActivityOpts.
func (f *fakeUserRepoForActivity) seedHuman(tenantID uuid.UUID, email string) uuid.UUID {
	id := uuid.New()
	f.byID[id] = &repository.User{
		ID:       id,
		TenantID: tenantID,
		Username: email,
		Email:    email,
		IsActive: true,
		Kind:     "human",
	}
	return id
}

// seedShadow inserts a service-account shadow user for the given tenant.
func (f *fakeUserRepoForActivity) seedShadow(tenantID uuid.UUID) uuid.UUID {
	id := uuid.New()
	f.byID[id] = &repository.User{
		ID:       id,
		TenantID: tenantID,
		Username: "shadow:" + id.String(),
		Kind:     "service_account",
	}
	return id
}

// GetUserAnyKind looks up a user by ID regardless of kind.
func (f *fakeUserRepoForActivity) GetUserAnyKind(_ context.Context, id uuid.UUID) (*repository.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

// ── fakeAuditClient ───────────────────────────────────────────────────────────

// fakeAuditClient is a hand-written implementation of auditv1.AuditServiceClient.
// Only GetNotifications is used by ActivityService; all other methods return
// Unimplemented so tests fail loudly if ActivityService calls the wrong RPC.
//
// notifs is the canned response returned by GetNotifications. notifErr, when
// non-nil, is returned instead so tests can simulate audit backend failures.
type fakeAuditClient struct {
	notifs   []*auditv1.NotificationEvent
	notifErr error
}

// GetNotifications returns the canned notifications response. The tenant_id and
// actor_id on the request are not validated here — filtering is the
// ActivityService's responsibility and is tested via the fakeUserRepo state.
func (f *fakeAuditClient) GetNotifications(_ context.Context, _ *auditv1.GetNotificationsRequest, _ ...grpc.CallOption) (*auditv1.GetNotificationsResponse, error) {
	if f.notifErr != nil {
		return nil, f.notifErr
	}
	return &auditv1.GetNotificationsResponse{
		Notifications: f.notifs,
		NextPageToken: "",
		UnreadCount:   int32(len(f.notifs)),
	}, nil
}

// ── Remaining methods return Unimplemented so tests fail loudly ──────────────

// ListAuditEvents is part of the (recently extended) auditv1.AuditServiceClient
// interface but is not used by ActivityService; the stub reports Unimplemented
// like the other unused methods so a test that reaches it fails loudly.
func (f *fakeAuditClient) ListAuditEvents(_ context.Context, _ *auditv1.ListAuditEventsRequest, _ ...grpc.CallOption) (*auditv1.ListAuditEventsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetBuildHistory(_ context.Context, _ *auditv1.GetBuildHistoryRequest, _ ...grpc.CallOption) (*auditv1.GetBuildHistoryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetDailyPullCount(_ context.Context, _ *auditv1.GetDailyPullCountRequest, _ ...grpc.CallOption) (*auditv1.GetDailyPullCountResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetRepoActivity(_ context.Context, _ *auditv1.GetRepoActivityRequest, _ ...grpc.CallOption) (*auditv1.GetRepoActivityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetAnalytics(_ context.Context, _ *auditv1.GetAnalyticsRequest, _ ...grpc.CallOption) (*auditv1.GetAnalyticsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetLastTenantPush(_ context.Context, _ *auditv1.GetLastTenantPushRequest, _ ...grpc.CallOption) (*auditv1.GetLastTenantPushResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}

// Audit export config trio added to AuditServiceClient post-FE-API-018 —
// stubbed Unimplemented here so the fake keeps satisfying the interface.
// REM-018 doesn't touch this surface; the stubs are a "while we're here"
// build-unstuck so the rest of the service test package can run.
func (f *fakeAuditClient) GetAuditExportConfig(_ context.Context, _ *auditv1.GetAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.AuditExportConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) PutAuditExportConfig(_ context.Context, _ *auditv1.PutAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.AuditExportConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) DeleteAuditExportConfig(_ context.Context, _ *auditv1.DeleteAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.DeleteAuditExportConfigResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) TestAuditExportConfig(_ context.Context, _ *auditv1.TestAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.TestAuditExportConfigResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) DrainAuditExportDLX(_ context.Context, _ *auditv1.DrainAuditExportDLXRequest, _ ...grpc.CallOption) (*auditv1.DrainAuditExportDLXResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetUserNotificationPreferences(_ context.Context, _ *auditv1.GetUserNotificationPreferencesRequest, _ ...grpc.CallOption) (*auditv1.GetUserNotificationPreferencesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) UpdateUserNotificationPreferences(_ context.Context, _ *auditv1.UpdateUserNotificationPreferencesRequest, _ ...grpc.CallOption) (*auditv1.UpdateUserNotificationPreferencesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}

// FUT-019 Phase 3 email-channel RPCs — stubbed; ActivityService never calls them
// but they're part of the AuditServiceClient interface, so the fake must satisfy them.
func (f *fakeAuditClient) GetEmailTransportConfig(_ context.Context, _ *auditv1.GetEmailTransportConfigRequest, _ ...grpc.CallOption) (*auditv1.EmailTransportConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) PutEmailTransportConfig(_ context.Context, _ *auditv1.PutEmailTransportConfigRequest, _ ...grpc.CallOption) (*auditv1.EmailTransportConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) SendTestEmail(_ context.Context, _ *auditv1.SendTestEmailRequest, _ ...grpc.CallOption) (*auditv1.SendTestEmailResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) ListEmailDeliveries(_ context.Context, _ *auditv1.ListEmailDeliveriesRequest, _ ...grpc.CallOption) (*auditv1.ListEmailDeliveriesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) GetNotificationWebhookConfig(_ context.Context, _ *auditv1.GetNotificationWebhookConfigRequest, _ ...grpc.CallOption) (*auditv1.NotificationWebhookConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) PutNotificationWebhookConfig(_ context.Context, _ *auditv1.PutNotificationWebhookConfigRequest, _ ...grpc.CallOption) (*auditv1.NotificationWebhookConfig, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}
func (f *fakeAuditClient) SendTestNotificationWebhook(_ context.Context, _ *auditv1.SendTestNotificationWebhookRequest, _ ...grpc.CallOption) (*auditv1.SendTestNotificationWebhookResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by ActivityService")
}

// ── Test harness ──────────────────────────────────────────────────────────────

// activityFakes bundles the fakes needed by newActivityService.
type activityFakes struct {
	userRepo *fakeUserRepoForActivity
	audit    *fakeAuditClient
}

// newActivityService builds an ActivityService wired to in-memory fakes.
// The returned activityFakes exposes the fakes so tests can seed state and
// assert behaviour.
func newActivityService(_ *testing.T, _ context.Context) (*ActivityService, *activityFakes) {
	ur := newFakeUserRepoForActivity()
	ac := &fakeAuditClient{}
	fakes := &activityFakes{userRepo: ur, audit: ac}
	svc := NewActivityService(ur, ac)
	return svc, fakes
}

// makeNotif is a helper that constructs a NotificationEvent for the given
// actor with the supplied action and optional metadata. The occurred_at is set
// to now so pagination cursor tests don't need to manipulate timestamps.
func makeNotif(actorID, action string, meta map[string]string) *auditv1.NotificationEvent {
	if meta == nil {
		meta = map[string]string{}
	}
	return &auditv1.NotificationEvent{
		EventId:       uuid.New().String(),
		EventType:     action,
		OccurredAt:    timestamppb.New(time.Now()),
		ActorId:       actorID,
		ActorUsername: actorID,
		Title:         action,
		Summary:       action,
		Metadata:      meta,
	}
}

// ── T9: cross-tenant 404 ──────────────────────────────────────────────────────

// TestActivity_CrossTenant404_T9 verifies that listing activity for a target
// that exists but belongs to a different tenant returns NotFound. The error
// must be identical to the "user genuinely not found" path so an attacker
// cannot use the status code to determine whether the target user exists in
// another tenant (spec §5.3 step 2, security finding M4).
func TestActivity_CrossTenant404_T9(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenantA, tenantB := uuid.New(), uuid.New()
	// adminA is an admin in tenantA.
	adminA := fakes.userRepo.seedHuman(tenantA, "a@x.com")
	// targetB belongs to tenantB — a different tenant.
	targetB := fakes.userRepo.seedHuman(tenantB, "b@x.com")

	_, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   adminA,
		CallerTenantID: tenantA,
		CallerIsAdmin:  true,
		TargetUserID:   targetB,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"cross-tenant target must return NotFound, not PermissionDenied or any other code")
}

// ── M4a: non-admin querying another user → 404 ───────────────────────────────

// TestActivity_NonAdminQueryingOther404 verifies that a non-admin caller who
// requests another user's activity receives NotFound — never Forbidden —
// so that user existence in the tenant cannot be inferred from the status code
// (security finding M4).
func TestActivity_NonAdminQueryingOther404(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	caller := fakes.userRepo.seedHuman(tenant, "caller@x.com")
	other := fakes.userRepo.seedHuman(tenant, "other@x.com")

	_, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   caller,
		CallerTenantID: tenant,
		CallerIsAdmin:  false,
		TargetUserID:   other,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"non-admin querying another user must return NotFound, not Forbidden")
}

// ── M4b: non-admin querying themselves → success ──────────────────────────────

// TestActivity_SelfQueryWorks verifies that a non-admin caller can query their
// own activity, which is the primary self-service use case for this endpoint.
func TestActivity_SelfQueryWorks(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	caller := fakes.userRepo.seedHuman(tenant, "me@x.com")

	// Seed one notification that belongs to this actor.
	fakes.audit.notifs = []*auditv1.NotificationEvent{
		makeNotif(caller.String(), "push.image", map[string]string{
			"repo":       "myorg/myapp",
			"source_ip":  "1.2.3.4",
			"api_key_id": "key-abc",
			"outcome":    "success",
		}),
	}

	activities, nextToken, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   caller,
		CallerTenantID: tenant,
		CallerIsAdmin:  false,
		TargetUserID:   caller, // querying self
	})
	require.NoError(t, err)
	require.Empty(t, nextToken, "fake client returns empty next_page_token")
	require.Len(t, activities, 1, "expected one activity for the self-query")

	act := activities[0]
	require.Equal(t, "push.image", act.Action)
	require.Equal(t, "myorg/myapp", act.Repo)
	require.Equal(t, "1.2.3.4", act.SourceIP)
	require.Equal(t, "key-abc", act.APIKeyID)
	require.Equal(t, "success", act.Status)
	require.False(t, act.At.IsZero(), "occurred_at must be set")
}

// ── M4c: admin querying another user in the same tenant → success ─────────────

// TestActivity_AdminQueryingAnotherInTenant verifies that a workspace-admin
// can query another user's activity within the same tenant.
func TestActivity_AdminQueryingAnotherInTenant(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	admin := fakes.userRepo.seedHuman(tenant, "admin@x.com")
	target := fakes.userRepo.seedHuman(tenant, "target@x.com")

	// Two events: one for the target, one for the admin. Only the target's
	// event should appear in the response.
	fakes.audit.notifs = []*auditv1.NotificationEvent{
		makeNotif(target.String(), "pull.image", map[string]string{"outcome": "success"}),
		makeNotif(admin.String(), "pull.image", map[string]string{"outcome": "success"}),
	}

	activities, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   admin,
		CallerTenantID: tenant,
		CallerIsAdmin:  true,
		TargetUserID:   target,
	})
	require.NoError(t, err)
	require.Len(t, activities, 1, "only the target's event should be returned, not the admin's")
	require.Equal(t, "pull.image", activities[0].Action)
}

// ── Not-found target → 404 ────────────────────────────────────────────────────

// TestActivity_UnknownTarget404 verifies that a completely unknown target
// user ID returns the same NotFound error as the cross-tenant case, ensuring
// the two negative paths are indistinguishable.
func TestActivity_UnknownTarget404(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	caller := fakes.userRepo.seedHuman(tenant, "caller@x.com")

	_, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   caller,
		CallerTenantID: tenant,
		CallerIsAdmin:  true,
		TargetUserID:   uuid.New(), // non-existent ID
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"unknown target ID must return NotFound, identical to cross-tenant path")
}

// ── Shadow user (SA) as target → allowed for admins ───────────────────────────

// TestActivity_ShadowUserTarget verifies that a workspace-admin can query the
// activity feed for a service-account shadow user. Shadow users are valid
// principals because their actions appear in the audit trail via the shadow
// user ID.
func TestActivity_ShadowUserTarget(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	admin := fakes.userRepo.seedHuman(tenant, "admin@x.com")
	// seedShadow creates a service_account kind user in the same tenant.
	shadowID := fakes.userRepo.seedShadow(tenant)

	fakes.audit.notifs = []*auditv1.NotificationEvent{
		makeNotif(shadowID.String(), "push.image", map[string]string{"outcome": "success"}),
	}

	activities, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   admin,
		CallerTenantID: tenant,
		CallerIsAdmin:  true,
		TargetUserID:   shadowID,
	})
	require.NoError(t, err)
	require.Len(t, activities, 1, "admin must be able to query a shadow user's activity")
}

// ── Audit backend error propagates ────────────────────────────────────────────

// TestActivity_AuditBackendError verifies that an error from the audit gRPC
// client is propagated directly to the caller so the HTTP handler can return
// the appropriate gRPC-mapped status.
func TestActivity_AuditBackendError(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newActivityService(t, ctx)

	tenant := uuid.New()
	caller := fakes.userRepo.seedHuman(tenant, "caller@x.com")

	// Inject a canned audit backend error.
	fakes.audit.notifErr = status.Error(codes.Unavailable, "audit service down")

	_, _, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   caller,
		CallerTenantID: tenant,
		CallerIsAdmin:  false,
		TargetUserID:   caller,
	})
	require.Error(t, err, "audit backend error must propagate")
	require.Equal(t, codes.Unavailable, status.Code(err))
}
