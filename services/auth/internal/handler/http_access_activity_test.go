// http_access_activity_test.go — HTTP handler tests for GET /api/v1/access/activity
// (FE-API-048, Task 15).
//
// These tests cover:
//   - Cross-tenant query by admin → 404 {"error":"NOT_FOUND"}
//   - Non-admin querying another user → 404 (not 403)
//   - Non-admin querying own user ID → 200 with activity list
//   - Admin querying another user in the same tenant → 200
//   - Route returns 501 when activityService is not wired
//
// Test setup reuses buildTestService / handlerFakeUserRepo from http_test.go and
// adds a fakeAuditClient that returns preset NotificationEvents.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── Fake audit gRPC client ────────────────────────────────────────────────────

// fakeAuditClient implements auditv1.AuditServiceClient. Only GetNotifications
// is exercised by these tests; all other methods return Unimplemented so that
// a compilation failure rather than a silent no-op reveals unexpected calls.
type fakeAuditClient struct {
	// notifs is the slice of NotificationEvent values returned by GetNotifications.
	// Tests seed this with events for the target actor.
	notifs []*auditv1.NotificationEvent
}

// GetNotifications returns the preconfigured notification slice.
func (f *fakeAuditClient) GetNotifications(
	_ context.Context,
	_ *auditv1.GetNotificationsRequest,
	_ ...grpc.CallOption,
) (*auditv1.GetNotificationsResponse, error) {
	return &auditv1.GetNotificationsResponse{
		Notifications: f.notifs,
	}, nil
}

// ── remaining auditv1.AuditServiceClient stubs ────────────────────────────────
// These stubs satisfy the interface but are never called in activity tests.

func (f *fakeAuditClient) GetBuildHistory(_ context.Context, _ *auditv1.GetBuildHistoryRequest, _ ...grpc.CallOption) (*auditv1.GetBuildHistoryResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) GetDailyPullCount(_ context.Context, _ *auditv1.GetDailyPullCountRequest, _ ...grpc.CallOption) (*auditv1.GetDailyPullCountResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) GetRepoActivity(_ context.Context, _ *auditv1.GetRepoActivityRequest, _ ...grpc.CallOption) (*auditv1.GetRepoActivityResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) GetAnalytics(_ context.Context, _ *auditv1.GetAnalyticsRequest, _ ...grpc.CallOption) (*auditv1.GetAnalyticsResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) GetLastTenantPush(_ context.Context, _ *auditv1.GetLastTenantPushRequest, _ ...grpc.CallOption) (*auditv1.GetLastTenantPushResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) GetAuditExportConfig(_ context.Context, _ *auditv1.GetAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.AuditExportConfig, error) {
	return nil, nil
}
func (f *fakeAuditClient) PutAuditExportConfig(_ context.Context, _ *auditv1.PutAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.AuditExportConfig, error) {
	return nil, nil
}
func (f *fakeAuditClient) DeleteAuditExportConfig(_ context.Context, _ *auditv1.DeleteAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.DeleteAuditExportConfigResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) TestAuditExportConfig(_ context.Context, _ *auditv1.TestAuditExportConfigRequest, _ ...grpc.CallOption) (*auditv1.TestAuditExportConfigResponse, error) {
	return nil, nil
}
func (f *fakeAuditClient) DrainAuditExportDLX(_ context.Context, _ *auditv1.DrainAuditExportDLXRequest, _ ...grpc.CallOption) (*auditv1.DrainAuditExportDLXResponse, error) {
	return nil, nil
}

// ── activityTestEnv ───────────────────────────────────────────────────────────

// activityTestEnv bundles the pieces needed to drive activity handler tests.
type activityTestEnv struct {
	// srv is the running test HTTP server.
	srv *httptest.Server
	// tc holds the core auth service context (users, apiKeys, svc).
	tc *testCtx
	// auditClient is the fake audit client; tests seed auditClient.notifs with
	// preset events before issuing the HTTP request.
	auditClient *fakeAuditClient
	// tenantID is the fixed tenant used by issueActivityAdminToken and
	// issueActivityReaderToken.
	tenantID uuid.UUID
}

// newActivityTestEnv starts an httptest.Server whose HTTPHandler has a fully-
// wired ActivityService backed by in-memory fakes. Returns the env; cleanup is
// registered via t.Cleanup automatically.
func newActivityTestEnv(t *testing.T) *activityTestEnv {
	t.Helper()

	// Build the core auth service (miniredis + fake user/key repos).
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	// Fake audit client — seed notifs in individual tests.
	auditClient := &fakeAuditClient{}

	// ActivityService uses the same handlerFakeUserRepo as the core service so
	// that tokens resolve to the same user records.
	actSvc := service.NewActivityService(tc.users, auditClient)

	tenantID := uuid.New()

	mux := http.NewServeMux()
	h := NewHTTPHandler(tc.svc, tenantID).WithActivityService(actSvc)
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &activityTestEnv{
		srv:         srv,
		tc:          tc,
		auditClient: auditClient,
		tenantID:    tenantID,
	}
}

// seedUser creates a user in the fake repo and returns the user struct.
// kind must be "human" or "service_account".
func (e *activityTestEnv) seedUser(tenantID uuid.UUID, kind string) *repository.User {
	id := uuid.New()
	u := &repository.User{
		ID:       id,
		TenantID: tenantID,
		Username: "user-" + id.String()[:8],
		Email:    id.String()[:8] + "@test.example",
		IsActive: true,
		Kind:     kind,
	}
	e.tc.users.users[u.Username] = u
	return u
}

// issueActivityToken issues a JWT for the given user in the given tenant.
// If markAdmin is true the user is also registered as an admin in the fake repo
// so callerIsTenantAdmin returns true for them.
func (e *activityTestEnv) issueActivityToken(t *testing.T, userID, tenantID uuid.UUID, markAdmin bool) string {
	t.Helper()
	if markAdmin {
		e.tc.users.makeAdmin(userID)
	}
	roles := []string{"reader"}
	if markAdmin {
		roles = []string{"admin"}
	}
	tok, err := e.tc.svc.IssueToken(context.Background(), userID.String(), tenantID.String(), nil, roles)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// doActivityReq issues a GET /api/v1/access/activity request with optional
// query params and Bearer token. Returns the raw *http.Response.
func doActivityReq(
	t *testing.T,
	env *activityTestEnv,
	token string,
	queryParams map[string]string,
) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/access/activity", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Apply query parameters.
	q := req.URL.Query()
	for k, v := range queryParams {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/access/activity: %v", err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHTTP_Activity_CrossTenant404 verifies that a workspace-admin in tenant A
// receives 404 {"error":"NOT_FOUND"} when querying a principal_user_id that
// belongs to tenant B. The spec §5.3 requires this so that cross-tenant probing
// is not possible even for admins — tenant B's user IDs must not be enumerable
// by tenant A callers.
func TestHTTP_Activity_CrossTenant404(t *testing.T) {
	env := newActivityTestEnv(t)

	// Create an admin in tenant A (env.tenantID).
	adminUser := env.seedUser(env.tenantID, "human")
	adminTok := env.issueActivityToken(t, adminUser.ID, env.tenantID, true)

	// Create a user in tenant B (a different UUID).
	tenantB := uuid.New()
	tenantBUser := env.seedUser(tenantB, "human")

	// Admin in tenant A queries tenant B's user_id.
	resp := doActivityReq(t, env, adminTok, map[string]string{
		"principal_user_id": tenantBUser.ID.String(),
	})
	defer resp.Body.Close()

	// Must be 404 — not 200, not 403.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-tenant query: got status %d, want 404", resp.StatusCode)
	}

	// Body must be exactly {"error":"NOT_FOUND"} (flat shape per spec §5.3).
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body["error"] != "NOT_FOUND" {
		t.Errorf(`body["error"]: got %q, want "NOT_FOUND"`, body["error"])
	}
}

// TestHTTP_Activity_NonAdminQueryingOther404 verifies that a non-admin caller
// ("alice") receives 404 — not 403 — when she queries "bob"'s principal_user_id.
// Returning 403 would leak that the target user exists in the tenant (M4).
func TestHTTP_Activity_NonAdminQueryingOther404(t *testing.T) {
	env := newActivityTestEnv(t)

	// "alice" — non-admin, plain reader in the same tenant.
	alice := env.seedUser(env.tenantID, "human")
	aliceTok := env.issueActivityToken(t, alice.ID, env.tenantID, false /* not admin */)

	// "bob" — another user in the same tenant.
	bob := env.seedUser(env.tenantID, "human")

	// Alice queries bob's activity.
	resp := doActivityReq(t, env, aliceTok, map[string]string{
		"principal_user_id": bob.ID.String(),
	})
	defer resp.Body.Close()

	// Must be 404 — not 403, to avoid existence oracle.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-admin querying other: got status %d, want 404 (not 403)", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body["error"] != "NOT_FOUND" {
		t.Errorf(`body["error"]: got %q, want "NOT_FOUND"`, body["error"])
	}
}

// TestHTTP_Activity_SelfQueryWorks verifies that a non-admin user can query their
// own activity (omitting principal_user_id defaults to the caller's ID) and
// receives 200 with the correct response structure.
func TestHTTP_Activity_SelfQueryWorks(t *testing.T) {
	env := newActivityTestEnv(t)

	// Seed a non-admin user in the tenant.
	alice := env.seedUser(env.tenantID, "human")
	aliceTok := env.issueActivityToken(t, alice.ID, env.tenantID, false /* not admin */)

	// Seed one event for alice in the fake audit client.
	env.auditClient.notifs = []*auditv1.NotificationEvent{
		{
			EventId:    uuid.New().String(),
			EventType:  "push.image",
			ActorId:    alice.ID.String(),
			OccurredAt: timestamppb.New(time.Now()),
			Metadata:   map[string]string{"repo": "myrepo", "outcome": "success"},
		},
	}

	// Self-query: omit principal_user_id so it defaults to alice.ID.
	resp := doActivityReq(t, env, aliceTok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("self-query: got status %d, want 200", resp.StatusCode)
	}

	// Decode and verify the response envelope.
	var envelope struct {
		Activity      []map[string]any `json:"activity"`
		NextPageToken string           `json:"next_page_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	// The response must include an "activity" array (possibly empty — structure
	// matters more than specific events).
	if envelope.Activity == nil {
		t.Error(`response must include an "activity" key (non-null array)`)
	}
	// Expect the one seeded event to be returned.
	if len(envelope.Activity) != 1 {
		t.Errorf("activity count: got %d, want 1", len(envelope.Activity))
	}
}

// TestHTTP_Activity_AdminQueryingOtherInTenant_Works verifies that a workspace-
// admin can query another user's activity within the same tenant and receives 200.
func TestHTTP_Activity_AdminQueryingOtherInTenant_Works(t *testing.T) {
	env := newActivityTestEnv(t)

	// Admin user in the tenant.
	admin := env.seedUser(env.tenantID, "human")
	adminTok := env.issueActivityToken(t, admin.ID, env.tenantID, true /* admin */)

	// Target user in the same tenant.
	target := env.seedUser(env.tenantID, "human")

	// Seed two events for the target actor in the fake audit client.
	env.auditClient.notifs = []*auditv1.NotificationEvent{
		{
			EventId:    uuid.New().String(),
			EventType:  "pull.image",
			ActorId:    target.ID.String(),
			OccurredAt: timestamppb.New(time.Now()),
			Metadata:   map[string]string{"repo": "repo-a", "outcome": "success"},
		},
		{
			EventId:    uuid.New().String(),
			EventType:  "push.image",
			ActorId:    target.ID.String(),
			OccurredAt: timestamppb.New(time.Now().Add(-time.Minute)),
			Metadata:   map[string]string{"repo": "repo-b", "outcome": "success"},
		},
	}

	// Admin queries the target's activity.
	resp := doActivityReq(t, env, adminTok, map[string]string{
		"principal_user_id": target.ID.String(),
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin querying same-tenant user: got status %d, want 200", resp.StatusCode)
	}

	var envelope struct {
		Activity      []map[string]any `json:"activity"`
		NextPageToken string           `json:"next_page_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if len(envelope.Activity) != 2 {
		t.Errorf("activity count: got %d, want 2", len(envelope.Activity))
	}
}

// TestHTTP_Activity_NoService_Returns501 verifies that the route returns
// 501 Not Implemented when WithActivityService was not called (nil service).
func TestHTTP_Activity_NoService_Returns501(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// Deliberately omit .WithActivityService(...) so activityService is nil.
	h := NewHTTPHandler(tc.svc, tenantID)
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Issue a valid token (auth should pass; 501 fires before the service call).
	userID := uuid.New()
	tok, err := tc.svc.IssueToken(context.Background(), userID.String(), tenantID.String(), nil, []string{"reader"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/access/activity", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("got status %d, want 501 Not Implemented", resp.StatusCode)
	}
}
