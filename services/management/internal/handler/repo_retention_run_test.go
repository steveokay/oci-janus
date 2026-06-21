// Tests for the FE-API-040 management routes: POST .../policies/retention/run
// and GET .../policies/retention/runs/{run_id}. We need both the normal
// repo-role auth fakes (which understand admin/writer/reader tokens) AND a
// wired-in gc client, so this file ships its own newRetentionRunEnv helper
// instead of reusing admin_gc_test.go's newGCEnv (which is platform-admin
// flavoured).
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// newRetentionRunEnv stands up a testEnv with the normal repo-role auth fakes
// PLUS a wired gc client backed by fakeGCServer (defined in admin_gc_test.go).
// Mirrors newTestEnv otherwise.
func newRetentionRunEnv(t *testing.T) (*testEnv, *fakeGCServer) {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
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
		nil, // publisher
		"",
		healthpb.NewHealthClient(dial(authLis)),
	).WithGCClient(gcv1.NewGCServiceClient(dial(gcLis)))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeGC
}

const retentionRunPath = "/api/v1/repositories/myorg/myrepo/policies/retention/run"

// ── POST .../run ─────────────────────────────────────────────────────────────

// TestTriggerRetentionRun_GCUnset_returns404 verifies the route stays
// disabled when GC_GRPC_ADDR is unset. The default test env doesn't wire
// the gc client, so the disabled-check fires before the auth gate would
// reach the RBAC marker.
func TestTriggerRetentionRun_GCUnset_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, retentionRunPath, adminToken, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestTriggerRetentionRun_admin_returns202 verifies the happy path: admin
// hits the trigger and the fake gc returns a queued run_id.
func TestTriggerRetentionRun_admin_returns202(t *testing.T) {
	env, fake := newRetentionRunEnv(t)
	resp := env.post(t, retentionRunPath, adminToken, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	if fake.lastTriggerRetentionReq == nil {
		t.Fatal("gc should have been called")
	}
	if fake.lastTriggerRetentionReq.GetRepoId() == "" {
		t.Error("repo_id should be populated from findRepo")
	}
	if fake.lastTriggerRetentionReq.GetTriggeredBy() == "" {
		t.Error("triggered_by should be propagated from JWT")
	}
}

// TestTriggerRetentionRun_owner_returns202 verifies an owner role is allowed
// (owner > admin in the role hierarchy).
func TestTriggerRetentionRun_owner_returns202(t *testing.T) {
	env, _ := newRetentionRunEnv(t)
	resp := env.post(t, retentionRunPath, ownerToken, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 for owner, got %d", resp.StatusCode)
	}
}

// TestTriggerRetentionRun_writer_returns403 verifies writer is NOT enough —
// retention deletes manifests, so writer-on-repo is rejected.
func TestTriggerRetentionRun_writer_returns403(t *testing.T) {
	env, _ := newRetentionRunEnv(t)
	resp := env.post(t, retentionRunPath, writerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for writer, got %d", resp.StatusCode)
	}
}

// TestTriggerRetentionRun_reader_returns403.
func TestTriggerRetentionRun_reader_returns403(t *testing.T) {
	env, _ := newRetentionRunEnv(t)
	resp := env.post(t, retentionRunPath, readerToken, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for reader, got %d", resp.StatusCode)
	}
}

// TestTriggerRetentionRun_grpcFailedPrecondition_returns404 verifies the
// dispatcher-missing case surfaces as 404 (matching admin_gc's behaviour).
func TestTriggerRetentionRun_grpcFailedPrecondition_returns404(t *testing.T) {
	env, fake := newRetentionRunEnv(t)
	fake.triggerRetentionErr = status.Error(codes.FailedPrecondition, "gc dispatcher not configured")
	resp := env.post(t, retentionRunPath, adminToken, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestTriggerRetentionRun_grpcInvalidArgument_returns400 verifies a gRPC
// validation failure bubbles up as 400.
func TestTriggerRetentionRun_grpcInvalidArgument_returns400(t *testing.T) {
	env, fake := newRetentionRunEnv(t)
	fake.triggerRetentionErr = status.Error(codes.InvalidArgument, "triggered_by must be a UUID")
	resp := env.post(t, retentionRunPath, adminToken, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ── GET .../runs/{run_id} ────────────────────────────────────────────────────

const retentionRunStatusPath = "/api/v1/repositories/myorg/myrepo/policies/retention/runs/abc"

// TestGetRetentionRun_reader_returns200 verifies reader is sufficient for the
// status read.
func TestGetRetentionRun_reader_returns200(t *testing.T) {
	env, fake := newRetentionRunEnv(t)
	fake.getRetentionStatusReturn = &gcv1.RetentionRunSummary{
		RunId:           "abc",
		Mode:            "retention",
		Status:          "succeeded",
		ManifestsMarked: 5,
	}
	resp := env.get(t, retentionRunStatusPath, readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for reader, got %d", resp.StatusCode)
	}
}

// TestGetRetentionRun_notFound_returns404.
func TestGetRetentionRun_notFound_returns404(t *testing.T) {
	env, fake := newRetentionRunEnv(t)
	fake.getRetentionStatusErr = status.Error(codes.NotFound, "run not found")
	resp := env.get(t, retentionRunStatusPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}
