// Tests for /api/v1/admin/tenants/{tenantID} GET (FE-API-028) and PATCH
// (FE-API-029). Both routes are gated by the platform-admin marker grant —
// see admin_tenants.go — so the test harness here stands up an env where the
// JWT-bound user holds (org=*, admin).
//
// Each test wires a bufconn-backed tenant + metadata + auth + audit so the
// full HTTP → gRPC → response composition is exercised end-to-end. The
// fakes below are scoped to this file via package-level overrides so they
// don't disturb the simpler newTestEnv used by other suites.
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const (
	// platformAdminToken is a stand-in JWT recognised by adminFakeAuthServer.
	// The corresponding user holds the FE-API-028/029 marker grant
	// (org=*, admin) so requirePlatformAdmin lets the request through.
	platformAdminToken = "platform-admin-token"
	platformAdminUser  = "00000000-0000-0000-0000-0000000000aa"
	// detailTenantID is the target tenant the GET / PATCH tests operate on.
	detailTenantID = "00000000-0000-0000-0000-00000000beef"
)

// adminFakeAuthServer is the auth-side fake for this suite. It recognises
// platformAdminToken (and admin/reader tokens for the RBAC denial test). We
// keep this private to this file rather than extending the package-level
// fakeAuthServer so the marker grant doesn't leak into unrelated suites.
type adminFakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *adminFakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case platformAdminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: platformAdminUser}, nil
	case readerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "reader-user"}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *adminFakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	switch req.GetUserId() {
	case platformAdminUser:
		// The marker grant — see PENTEST-024 + admin_tenants.go.
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "marker", UserId: platformAdminUser, Role: "admin", ScopeType: "org", ScopeValue: "*"},
			},
		}, nil
	default:
		// Plain reader — no marker, so RBAC must refuse.
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"reader"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "r1", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	}
}

// adminFakeTenantServer covers GET / UpdateTenant for the admin routes. Each
// test slot is package-level so a single test can override behaviour without
// rebuilding the whole bufconn stack.
type adminFakeTenantServer struct {
	tenantv1.UnimplementedTenantServiceServer
}

var (
	adminTenantGet *tenantv1.Tenant
	adminTenantGetErr error

	// adminTenantUpdate is consulted on every UpdateTenant call. The function
	// is responsible for honouring the request (apply patch semantics) and
	// returning the updated proto OR an error. Tests assert wiring by
	// inspecting adminLastUpdateReq.
	adminTenantUpdate    func(req *tenantv1.UpdateTenantRequest) (*tenantv1.Tenant, error)
	adminLastUpdateReq   *tenantv1.UpdateTenantRequest
	adminLastUpdateMutex sync.Mutex
)

func (s *adminFakeTenantServer) GetTenant(_ context.Context, _ *tenantv1.GetTenantRequest) (*tenantv1.Tenant, error) {
	if adminTenantGetErr != nil {
		return nil, adminTenantGetErr
	}
	if adminTenantGet != nil {
		return adminTenantGet, nil
	}
	// Default happy-path tenant identity.
	return &tenantv1.Tenant{
		TenantId:     detailTenantID,
		Name:         "acme",
		Slug:         "acme",
		Plan:         "pro",
		Host:         "acme.registry.example.com",
		HostIsCustom: false,
		CreatedAt:    timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}, nil
}

func (s *adminFakeTenantServer) UpdateTenant(_ context.Context, req *tenantv1.UpdateTenantRequest) (*tenantv1.Tenant, error) {
	adminLastUpdateMutex.Lock()
	adminLastUpdateReq = req
	adminLastUpdateMutex.Unlock()
	if adminTenantUpdate != nil {
		return adminTenantUpdate(req)
	}
	// Default: apply patch to the GET response so detail-build tests can run
	// without bespoke override logic.
	base, _ := s.GetTenant(context.Background(), &tenantv1.GetTenantRequest{TenantId: req.GetTenantId()})
	if base == nil {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	out := *base
	if req.Name != nil {
		out.Name = req.GetName()
		out.Slug = req.GetName()
	}
	if req.Plan != nil {
		out.Plan = req.GetPlan()
	}
	return &out, nil
}

func (s *adminFakeTenantServer) DeleteTenant(_ context.Context, _ *tenantv1.DeleteTenantRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

// adminFakeMetaServer + adminFakeAuditServer cover the composition probes
// for FE-API-028. Each method returns whatever the package-level override
// stashes; nil means "use the canned default".
type adminFakeMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer
}

var adminTenantUsage *metadatav1.TenantUsage

func (s *adminFakeMetaServer) GetTenantUsage(_ context.Context, _ *metadatav1.GetTenantUsageRequest) (*metadatav1.TenantUsage, error) {
	if adminTenantUsage != nil {
		return adminTenantUsage, nil
	}
	return &metadatav1.TenantUsage{
		StorageUsedBytes:  4096,
		StorageQuotaBytes: 10 << 30,
		RepositoryCount:   5,
		OrganizationCount: 2,
	}, nil
}

// CountRepositories is unused on the admin path but the management.New
// wiring still pings it for /api/v1/stats elsewhere; include a benign stub
// so the gRPC server can satisfy any incidental call without panicking.
func (s *adminFakeMetaServer) CountRepositories(_ context.Context, _ *metadatav1.CountRepositoriesRequest) (*metadatav1.CountRepositoriesResponse, error) {
	return &metadatav1.CountRepositoriesResponse{Count: 0}, nil
}

var adminUserCount int64 = 7

func (s *adminFakeAuthServer) CountTenantUsers(_ context.Context, _ *authv1.CountTenantUsersRequest) (*authv1.CountTenantUsersResponse, error) {
	return &authv1.CountTenantUsersResponse{Count: adminUserCount}, nil
}

type adminFakeAuditServer struct {
	auditv1.UnimplementedAuditServiceServer
}

var adminLastPushAt *time.Time

func (s *adminFakeAuditServer) GetLastTenantPush(_ context.Context, _ *auditv1.GetLastTenantPushRequest) (*auditv1.GetLastTenantPushResponse, error) {
	if adminLastPushAt == nil {
		return &auditv1.GetLastTenantPushResponse{}, nil
	}
	return &auditv1.GetLastTenantPushResponse{LastPushAt: timestamppb.New(*adminLastPushAt)}, nil
}

// adminFakePublisher records every published event so tests can assert that
// rename / plan-change patches emitted the expected RabbitMQ payload.
type adminFakePublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	routingKey string
	event      events.Event
}

func (p *adminFakePublisher) Publish(_ context.Context, routingKey string, e events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{routingKey: routingKey, event: e})
	return nil
}

func (p *adminFakePublisher) calls() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedEvent, len(p.events))
	copy(out, p.events)
	return out
}

// newAdminEnv stands up the bufconn stack with a tenant client wired in and
// returns an env plus the publisher so tests can inspect emitted events.
func newAdminEnv(t *testing.T) (*testEnv, *adminFakePublisher) {
	t.Helper()

	// Reset per-test overrides so cases run in isolation.
	t.Cleanup(func() {
		adminTenantGet = nil
		adminTenantGetErr = nil
		adminTenantUpdate = nil
		adminLastUpdateReq = nil
		adminTenantUsage = nil
		adminUserCount = 7
		adminLastPushAt = nil
	})

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &adminFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &adminFakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &adminFakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	tenantLis := bufconn.Listen(bufSize)
	tenantGRPC := grpc.NewServer()
	tenantv1.RegisterTenantServiceServer(tenantGRPC, &adminFakeTenantServer{})
	healthpb.RegisterHealthServer(tenantGRPC, &fakeHealthServer{})
	go func() { _ = tenantGRPC.Serve(tenantLis) }()
	t.Cleanup(tenantGRPC.Stop)

	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial bufconn: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	pub := &adminFakePublisher{}
	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil, // production publisher unused — we inject via WithPublisher below
		"",  // platformAdminTenantID unused (FE-API-028/029 use the marker)
		healthpb.NewHealthClient(dial(authLis)),
	).WithTenantClient(tenantv1.NewTenantServiceClient(dial(tenantLis))).WithPublisher(pub)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, pub
}

// patch sends a PATCH request — testEnv only provides get/post/del so the
// helper lives in this file.
func (e *testEnv) patch(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch, e.srv.URL+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	return resp
}

// ─── FE-API-028 GET ─────────────────────────────────────────────────────────

// TestAdminGetTenant_HappyPath_composesFromFourServices walks the full
// four-way composition: tenant identity + metadata usage + auth user count +
// audit last push. Every field on AdminTenantDetailResponse must be populated.
func TestAdminGetTenant_HappyPath_composesFromFourServices(t *testing.T) {
	pushed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	adminLastPushAt = &pushed
	adminUserCount = 11

	env, _ := newAdminEnv(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AdminTenantDetailResponse
	decodeJSON(t, resp, &body)

	if body.TenantID != detailTenantID || body.Name != "acme" || body.Plan != "pro" {
		t.Errorf("identity mismatch: %+v", body)
	}
	if body.Slug != "acme" || body.Host != "acme.registry.example.com" || body.HostIsCustom {
		t.Errorf("workspace fields mismatch: slug=%q host=%q custom=%v", body.Slug, body.Host, body.HostIsCustom)
	}
	if body.StorageUsedBytes != 4096 || body.RepositoryCount != 5 || body.OrganizationCount != 2 {
		t.Errorf("metadata aggregate mismatch: %+v", body)
	}
	if body.UserCount != 11 {
		t.Errorf("user count: got %d, want 11", body.UserCount)
	}
	if body.LastPushAt == nil || !body.LastPushAt.Equal(pushed) {
		t.Errorf("last_push_at: got %v, want %v", body.LastPushAt, pushed)
	}
}

// TestAdminGetTenant_ZeroUsage_returnsZerosNotNulls covers the lazy tenant
// case — metadata + audit return empty/no-rows; the response must surface
// zero values and last_push_at = null (the JSON encodes nil pointer as null).
func TestAdminGetTenant_ZeroUsage_returnsZerosNotNulls(t *testing.T) {
	adminTenantUsage = &metadatav1.TenantUsage{}
	adminUserCount = 0
	adminLastPushAt = nil

	env, _ := newAdminEnv(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AdminTenantDetailResponse
	decodeJSON(t, resp, &body)

	if body.StorageUsedBytes != 0 || body.StorageQuotaBytes != 0 ||
		body.RepositoryCount != 0 || body.OrganizationCount != 0 || body.UserCount != 0 {
		t.Errorf("expected all zero usage, got %+v", body)
	}
	if body.LastPushAt != nil {
		t.Errorf("expected nil LastPushAt, got %v", *body.LastPushAt)
	}
}

// TestAdminGetTenant_RBACDenied_returns403 verifies a plain reader (no marker)
// is rejected before reaching any gRPC composition probe.
func TestAdminGetTenant_RBACDenied_returns403(t *testing.T) {
	env, _ := newAdminEnv(t)
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── FE-API-029 PATCH ───────────────────────────────────────────────────────

// TestAdminUpdateTenant_BothFields_PatchesAndEmitsTwoEvents covers the happy
// path with both fields supplied: rename + plan change both emit their own
// RabbitMQ event so the audit trail distinguishes the two mutations.
func TestAdminUpdateTenant_BothFields_PatchesAndEmitsTwoEvents(t *testing.T) {
	env, pub := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"name":"acme-corp","plan":"enterprise"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.AdminTenantDetailResponse
	decodeJSON(t, resp, &body)
	if body.Name != "acme-corp" || body.Plan != "enterprise" {
		t.Errorf("patch shape: got %+v, want name=acme-corp plan=enterprise", body)
	}

	calls := pub.calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.routingKey] = true
	}
	if !seen[events.RoutingTenantRenamed] || !seen[events.RoutingTenantPlanChanged] {
		t.Errorf("expected both tenant.renamed and tenant.plan_changed, got %+v", calls)
	}
}

// TestAdminUpdateTenant_NameOnly_emitsRenamedEventOnly verifies the per-field
// event split: only the field actually mutated should fire an event.
func TestAdminUpdateTenant_NameOnly_emitsRenamedEventOnly(t *testing.T) {
	env, pub := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"name":"new-name"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	calls := pub.calls()
	if len(calls) != 1 || calls[0].routingKey != events.RoutingTenantRenamed {
		t.Errorf("expected single renamed event, got %+v", calls)
	}
}

// TestAdminUpdateTenant_PlanOnly_emitsPlanChangedEventOnly verifies the
// symmetric case.
func TestAdminUpdateTenant_PlanOnly_emitsPlanChangedEventOnly(t *testing.T) {
	env, pub := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"plan":"free"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	calls := pub.calls()
	if len(calls) != 1 || calls[0].routingKey != events.RoutingTenantPlanChanged {
		t.Errorf("expected single plan_changed event, got %+v", calls)
	}
}

// TestAdminUpdateTenant_NeitherField_returns400 verifies the "must supply at
// least one mutation" rule fires before any gRPC call.
func TestAdminUpdateTenant_NeitherField_returns400(t *testing.T) {
	env, _ := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken, `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminUpdateTenant_InvalidPlan_returns400 verifies plan allowlist.
func TestAdminUpdateTenant_InvalidPlan_returns400(t *testing.T) {
	env, _ := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"plan":"gold"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminUpdateTenant_InvalidName_returns400 ensures the BFF rejects bad
// names locally so we don't waste a gRPC round trip.
func TestAdminUpdateTenant_InvalidName_returns400(t *testing.T) {
	env, _ := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"name":"UPPER"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminUpdateTenant_DuplicateName_returns409 verifies an AlreadyExists
// from the tenant service maps to a 409 Conflict on the REST layer.
func TestAdminUpdateTenant_DuplicateName_returns409(t *testing.T) {
	adminTenantUpdate = func(_ *tenantv1.UpdateTenantRequest) (*tenantv1.Tenant, error) {
		return nil, status.Error(codes.AlreadyExists, "tenant name already in use")
	}
	env, _ := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken,
		`{"name":"clash"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

// TestAdminUpdateTenant_RBACDenied_returns403 verifies the marker gate for
// the PATCH route. Same negative test as the GET above; both routes must
// refuse non-platform-admins.
func TestAdminUpdateTenant_RBACDenied_returns403(t *testing.T) {
	env, _ := newAdminEnv(t)
	resp := env.patch(t, "/api/v1/admin/tenants/"+detailTenantID, readerToken,
		`{"plan":"pro"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── encoding sanity ────────────────────────────────────────────────────────

// TestAdminTenantDetail_LastPushNullSerialisation guards the JSON shape: a
// nil time pointer must round-trip as `null`, not `"0001-01-01..."`. The
// frontend treats a null differently from an epoch timestamp.
func TestAdminTenantDetail_LastPushNullSerialisation(t *testing.T) {
	d := handler.AdminTenantDetailResponse{
		AdminTenantResponse: handler.AdminTenantResponse{TenantID: "id"},
		LastPushAt:          nil,
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"last_push_at":null`)) {
		t.Errorf("expected last_push_at:null in JSON, got %s", string(b))
	}
}
