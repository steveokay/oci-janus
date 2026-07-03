// Tests for FE-API-032 GC status visibility (admin_gc.go). Mirrors
// the admin_tenants_test.go bufconn pattern: stand up fakes for the
// management handler's gRPC clients, drive HTTP requests through the
// real mux, and assert the response.
//
// Auth posture: every route requires the platform-admin marker
// (org=*, admin), so the test harness reuses the adminFakeAuthServer
// (defined in admin_tenants_test.go) that issues that grant for
// platformAdminToken. The readerToken path provides the negative
// case.
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeGCServer is the bufconn-backed GCService used by these tests.
// Per-test mutation goes through the package-level variables below so
// individual cases can simulate edge conditions (NotFound, RunNow
// error) without bringing up a fresh server.
type fakeGCServer struct {
	gcv1.UnimplementedGCServiceServer

	mu sync.Mutex

	statusReturn *gcv1.GCStatus
	statusErr    error

	runsReturn []*gcv1.GCRun
	runsNext   string
	runsErr    error
	lastList   *gcv1.ListRunsRequest

	runNowErr     error
	runNowReturn  *gcv1.RunNowResponse
	lastRunNowReq *gcv1.RunNowRequest

	// FE-API-040 — retention executor RPCs.
	triggerRetentionErr     error
	triggerRetentionReturn  *gcv1.TriggerRetentionRunResponse
	lastTriggerRetentionReq *gcv1.TriggerRetentionRunRequest

	getRetentionStatusErr     error
	getRetentionStatusReturn  *gcv1.RetentionRunSummary
	lastGetRetentionStatusReq *gcv1.GetRetentionRunStatusRequest

	// REM-013 gap 3 — retention savings aggregate.
	getSavingsErr    error
	getSavingsReturn *gcv1.TenantRetentionSavings
	lastGetSavingsReq *gcv1.GetTenantRetentionSavingsRequest
}

func (s *fakeGCServer) GetStatus(_ context.Context, _ *gcv1.GetStatusRequest) (*gcv1.GCStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	if s.statusReturn != nil {
		return s.statusReturn, nil
	}
	return &gcv1.GCStatus{}, nil
}

func (s *fakeGCServer) ListRuns(_ context.Context, req *gcv1.ListRunsRequest) (*gcv1.ListRunsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastList = req
	if s.runsErr != nil {
		return nil, s.runsErr
	}
	return &gcv1.ListRunsResponse{Runs: s.runsReturn, NextPageToken: s.runsNext}, nil
}

func (s *fakeGCServer) RunNow(_ context.Context, req *gcv1.RunNowRequest) (*gcv1.RunNowResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRunNowReq = req
	if s.runNowErr != nil {
		return nil, s.runNowErr
	}
	if s.runNowReturn != nil {
		return s.runNowReturn, nil
	}
	return &gcv1.RunNowResponse{RunId: "fake-run-id", Status: "queued"}, nil
}

// FE-API-040 — retention executor server stubs.
func (s *fakeGCServer) TriggerRetentionRun(_ context.Context, req *gcv1.TriggerRetentionRunRequest) (*gcv1.TriggerRetentionRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTriggerRetentionReq = req
	if s.triggerRetentionErr != nil {
		return nil, s.triggerRetentionErr
	}
	if s.triggerRetentionReturn != nil {
		return s.triggerRetentionReturn, nil
	}
	return &gcv1.TriggerRetentionRunResponse{RunId: "fake-retention-run", Status: "queued"}, nil
}

func (s *fakeGCServer) GetRetentionRunStatus(_ context.Context, req *gcv1.GetRetentionRunStatusRequest) (*gcv1.RetentionRunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGetRetentionStatusReq = req
	if s.getRetentionStatusErr != nil {
		return nil, s.getRetentionStatusErr
	}
	if s.getRetentionStatusReturn != nil {
		return s.getRetentionStatusReturn, nil
	}
	return &gcv1.RetentionRunSummary{RunId: req.GetRunId(), Mode: "retention", Status: "succeeded"}, nil
}

// REM-013 gap 3 — retention savings aggregate stub.
func (s *fakeGCServer) GetTenantRetentionSavings(_ context.Context, req *gcv1.GetTenantRetentionSavingsRequest) (*gcv1.TenantRetentionSavings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGetSavingsReq = req
	if s.getSavingsErr != nil {
		return nil, s.getSavingsErr
	}
	if s.getSavingsReturn != nil {
		return s.getSavingsReturn, nil
	}
	return &gcv1.TenantRetentionSavings{TenantId: req.GetTenantId()}, nil
}

// newGCEnv stands up a bufconn stack with a wired-in GCService client
// plus the platform-admin auth fake. Mirrors newAdminEnv but for the
// FE-API-032 surface — keeping the fakes separate from admin_tenants
// keeps the suites independent.
func newGCEnv(t *testing.T) (*testEnv, *fakeGCServer) {
	t.Helper()

	// Auth: reuse the marker-issuing adminFakeAuthServer from
	// admin_tenants_test.go so the platformAdminToken carries the
	// (org=*, admin) grant.
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &adminFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	fakeGC := &fakeGCServer{}
	gcLis := bufconn.Listen(bufSize)
	gcGRPC := grpc.NewServer()
	gcv1.RegisterGCServiceServer(gcGRPC, fakeGC)
	healthpb.RegisterHealthServer(gcGRPC, &fakeHealthServer{})
	go func() { _ = gcGRPC.Serve(gcLis) }()
	t.Cleanup(gcGRPC.Stop)

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

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil, // publisher not exercised
		"",
		healthpb.NewHealthClient(dial(authLis)),
	).WithGCClient(gcv1.NewGCServiceClient(dial(gcLis)))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeGC
}

// ─── status ─────────────────────────────────────────────────────────────────

// TestAdminGCStatus_GCUnset_returns404 verifies the route stays
// disabled when GC_GRPC_ADDR is unset (h.gc == nil). The disabled
// check fires before the RBAC marker gate, so the default test env's
// adminToken is sufficient here — we just need a request that gets
// past RequireAuth.
func TestAdminGCStatus_GCUnset_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/status", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestAdminGCStatus_HappyPath_returnsLastRunFields verifies the
// composition: every last_run_* field on the proto maps to the JSON
// equivalent.
func TestAdminGCStatus_HappyPath_returnsLastRunFields(t *testing.T) {
	completed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	env, fake := newGCEnv(t)
	fake.statusReturn = &gcv1.GCStatus{
		LastRunId:               "11111111-1111-1111-1111-111111111111",
		LastRunMode:             "full",
		LastRunStatus:           "succeeded",
		LastRunStartedAt:        timestamppb.New(completed.Add(-2 * time.Minute)),
		LastRunCompletedAt:      timestamppb.New(completed),
		LastRunDurationMs:       120_000,
		LastRunBlobsFreed:       42,
		LastRunManifestsDeleted: 7,
		LastRunBytesFreed:       1024,
		LastRunTriggeredBy:      "cron",
		NextScheduledAt:         timestamppb.New(completed.Add(24 * time.Hour)),
	}
	resp := env.get(t, "/api/v1/admin/gc/status", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.GCStatusResponse
	decodeJSON(t, resp, &body)
	if body.LastRunID == "" || body.LastRunMode != "full" || body.LastRunStatus != "succeeded" {
		t.Errorf("status fields: got %+v", body)
	}
	if body.LastRunBlobsFreed != 42 || body.LastRunManifestsDeleted != 7 {
		t.Errorf("counts: got blobs=%d manifests=%d", body.LastRunBlobsFreed, body.LastRunManifestsDeleted)
	}
	if body.NextScheduledAt == "" {
		t.Error("next_scheduled_at should be populated")
	}
}

// TestAdminGCStatus_RBACDenied_returns403 verifies the marker gate.
func TestAdminGCStatus_RBACDenied_returns403(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/status", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── runs list ──────────────────────────────────────────────────────────────

// TestAdminGCRuns_HappyPath_returnsRows verifies the list endpoint
// JSON shape — runs[] is non-nil even when empty, next_page_token is
// omitted when empty.
func TestAdminGCRuns_HappyPath_returnsRows(t *testing.T) {
	env, fake := newGCEnv(t)
	fake.runsReturn = []*gcv1.GCRun{
		{RunId: "a", Mode: "full", Status: "succeeded", RequestedAt: timestamppb.Now()},
		{RunId: "b", Mode: "dry-run", Status: "running", RequestedAt: timestamppb.Now()},
	}
	resp := env.get(t, "/api/v1/admin/gc/runs", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Runs          []handler.GCRunResponse `json:"runs"`
		NextPageToken string                  `json:"next_page_token,omitempty"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Runs) != 2 {
		t.Errorf("runs len: got %d, want 2", len(body.Runs))
	}
}

// TestAdminGCRuns_EmptyList_returnsNonNilArray verifies the wire
// shape never emits `null` for the runs field — the dashboard
// shouldn't have to guard against null.
func TestAdminGCRuns_EmptyList_returnsNonNilArray(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/runs", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	decodeJSON(t, resp, &raw)
	if string(raw["runs"]) != "[]" {
		t.Errorf("expected runs:[], got %s", string(raw["runs"]))
	}
}

// TestAdminGCRuns_BadPageToken_returns400 verifies the BFF rejects
// malformed cursors before the gRPC call.
func TestAdminGCRuns_BadPageToken_returns400(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/runs?page_token=@@@bad@@@", platformAdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminGCRuns_GRPCInvalidArgument_returns400 verifies a deeper
// page_token rejection (gc service surfaces INVALID_ARGUMENT) still
// maps onto a 400 at the REST layer.
func TestAdminGCRuns_GRPCInvalidArgument_returns400(t *testing.T) {
	env, fake := newGCEnv(t)
	fake.runsErr = status.Error(codes.InvalidArgument, "invalid page_token")
	// Use a token that passes the BFF's structural validator (alnum,
	// =-, etc) so the gRPC rejection is the one surfaced.
	resp := env.get(t, "/api/v1/admin/gc/runs?page_token=abcdef", platformAdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminGCRuns_LimitClamp_appliesAtBFF verifies the BFF caps
// page_size at 200 before the call so the gRPC client never sees a
// runaway value.
func TestAdminGCRuns_LimitClamp_appliesAtBFF(t *testing.T) {
	env, fake := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/runs?limit=5000", platformAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fake.lastList == nil || fake.lastList.GetPageSize() != 200 {
		got := int32(-1)
		if fake.lastList != nil {
			got = fake.lastList.GetPageSize()
		}
		t.Errorf("page_size: got %d, want 200", got)
	}
}

// TestAdminGCRuns_RBACDenied_returns403 mirrors the status route's
// marker test.
func TestAdminGCRuns_RBACDenied_returns403(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/runs", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── run-now ────────────────────────────────────────────────────────────────

// TestAdminGCRun_HappyPath_returns202 verifies the queue accept
// posture: 202 + {run_id, status:"queued"}.
func TestAdminGCRun_HappyPath_returns202(t *testing.T) {
	env, fake := newGCEnv(t)
	fake.runNowReturn = &gcv1.RunNowResponse{RunId: "queued-run-id", Status: "queued"}
	resp := env.post(t, "/api/v1/admin/gc/run", platformAdminToken, `{"mode":"full"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var body struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	decodeJSON(t, resp, &body)
	if body.RunID != "queued-run-id" || body.Status != "queued" {
		t.Errorf("body: got %+v", body)
	}
	if fake.lastRunNowReq.GetMode() != "full" {
		t.Errorf("forwarded mode: got %q, want full", fake.lastRunNowReq.GetMode())
	}
	// triggered_by must be the JWT user, propagated from RequireAuth.
	if !strings.EqualFold(fake.lastRunNowReq.GetTriggeredBy(), platformAdminUser) {
		t.Errorf("triggered_by: got %q, want %q", fake.lastRunNowReq.GetTriggeredBy(), platformAdminUser)
	}
}

// TestAdminGCRun_InvalidMode_returns400 verifies the BFF mode
// allowlist fires before the gRPC call.
func TestAdminGCRun_InvalidMode_returns400(t *testing.T) {
	env, fake := newGCEnv(t)
	resp := env.post(t, "/api/v1/admin/gc/run", platformAdminToken, `{"mode":"obliterate"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if fake.lastRunNowReq != nil {
		t.Error("gc service should not have been called for invalid mode")
	}
}

// TestAdminGCRun_MalformedBody_returns400.
func TestAdminGCRun_MalformedBody_returns400(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.post(t, "/api/v1/admin/gc/run", platformAdminToken, `{"mode":`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminGCRun_RBACDenied_returns403.
func TestAdminGCRun_RBACDenied_returns403(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.post(t, "/api/v1/admin/gc/run", readerToken, `{"mode":"full"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestAdminGCRun_GRPCInvalidArgument_returns400 verifies a gRPC
// validation failure on RunNow surfaces as 400.
func TestAdminGCRun_GRPCInvalidArgument_returns400(t *testing.T) {
	env, fake := newGCEnv(t)
	fake.runNowErr = status.Error(codes.InvalidArgument, "mode must be one of dry-run|manifests|blobs|full")
	resp := env.post(t, "/api/v1/admin/gc/run", platformAdminToken, `{"mode":"full"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestAdminGCRun_GRPCFailedPrecondition_returns404 verifies that
// FailedPrecondition (e.g. dispatcher not configured on gc side)
// surfaces as 404 "route disabled" on the REST surface — the
// dashboard treats this the same as GC_GRPC_ADDR being unset.
func TestAdminGCRun_GRPCFailedPrecondition_returns404(t *testing.T) {
	env, fake := newGCEnv(t)
	fake.runNowErr = status.Error(codes.FailedPrecondition, "dispatcher missing")
	resp := env.post(t, "/api/v1/admin/gc/run", platformAdminToken, `{"mode":"full"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}
