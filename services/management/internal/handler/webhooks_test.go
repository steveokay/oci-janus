package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/emptypb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// errWebhookServer is a fake webhook gRPC server that returns a fixed error for
// every mutating RPC. Used to exercise error-translation paths in the management
// HTTP handler without standing up a real webhook service.
type errWebhookServer struct {
	webhookv1.UnimplementedWebhookServiceServer
	err error
}

func (s *errWebhookServer) TestDispatch(_ context.Context, _ *webhookv1.TestDispatchRequest) (*webhookv1.TestDispatchResponse, error) {
	return nil, s.err
}

func (s *errWebhookServer) CreateEndpoint(_ context.Context, _ *webhookv1.CreateEndpointRequest) (*webhookv1.Endpoint, error) {
	return nil, s.err
}

func (s *errWebhookServer) DeleteEndpoint(_ context.Context, _ *webhookv1.DeleteEndpointRequest) (*emptypb.Empty, error) {
	return nil, s.err
}

func (s *errWebhookServer) UpdateEndpoint(_ context.Context, _ *webhookv1.UpdateEndpointRequest) (*webhookv1.Endpoint, error) {
	return nil, s.err
}

func (s *errWebhookServer) RotateEndpointSecret(_ context.Context, _ *webhookv1.RotateEndpointSecretRequest) (*emptypb.Empty, error) {
	return nil, s.err
}

func (s *errWebhookServer) ListDeliveries(_ *webhookv1.ListDeliveriesRequest, _ webhookv1.WebhookService_ListDeliveriesServer) error {
	return s.err
}

// newWebhookTestEnv spins up the full management handler (auth + meta + audit +
// webhook) wired with a provided webhook gRPC server. Mirrors newTestEnv but
// includes the extra webhook bufconn so webhook routes are exerciseable.
func newWebhookTestEnv(t *testing.T, wh webhookv1.WebhookServiceServer) *testEnv {
	t.Helper()

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

	startGRPC := func(reg func(*grpc.Server)) *bufconn.Listener {
		lis := bufconn.Listen(bufSize)
		srv := grpc.NewServer()
		reg(srv)
		healthpb.RegisterHealthServer(srv, &fakeHealthServer{})
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)
		return lis
	}

	authLis := startGRPC(func(s *grpc.Server) { authv1.RegisterAuthServiceServer(s, &fakeAuthServer{}) })
	metaLis := startGRPC(func(s *grpc.Server) { metadatav1.RegisterMetadataServiceServer(s, &fakeMetaServer{}) })
	auditLis := startGRPC(func(s *grpc.Server) { auditv1.RegisterAuditServiceServer(s, &fakeAuditServer{}) })
	whLis := startGRPC(func(s *grpc.Server) { webhookv1.RegisterWebhookServiceServer(s, wh) })

	authConn := dial(authLis)
	metaConn := dial(metaLis)
	auditConn := dial(auditLis)
	whConn := dial(whLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, // publisher
		"",  // platformAdminTenantID
		healthpb.NewHealthClient(authConn),
		healthpb.NewHealthClient(metaConn),
		healthpb.NewHealthClient(auditConn),
	).WithWebhookClient(webhookv1.NewWebhookServiceClient(whConn))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// ---------------------------------------------------------------------------
// PENTEST-031 — mapWebhookGRPCError must not echo gRPC InvalidArgument text
// ---------------------------------------------------------------------------

// TestWebhookTestDispatch_invalidArgument_doesNotLeakSSRFDetails verifies that
// when registry-webhook returns codes.InvalidArgument with an internal detail
// string (e.g. a blocked private IP from the SSRF guard), the management API
// returns the generic message "invalid request" and does NOT propagate the
// gRPC detail to the HTTP response body.
func TestWebhookTestDispatch_invalidArgument_doesNotLeakSSRFDetails(t *testing.T) {
	sensitiveDetail := "blocked private IP 10.20.30.40 (RFC1918)"

	env := newWebhookTestEnv(t, &errWebhookServer{
		err: status.Error(codes.InvalidArgument, sensitiveDetail),
	})

	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/webhooks/00000000-0000-0000-0000-000000000042/test", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/webhooks/00000000-0000-0000-0000-000000000042/test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp map[string]string
	if jsonErr := json.Unmarshal(body, &errResp); jsonErr != nil {
		t.Fatalf("response is not JSON: %s", body)
	}

	if msg, ok := errResp["error"]; !ok || msg != "invalid request" {
		t.Errorf("expected error=%q, got body=%s", "invalid request", body)
	}
	if bodyStr := string(body); contains(bodyStr, sensitiveDetail) {
		t.Errorf("response body must not contain sensitive detail %q, got: %s", sensitiveDetail, body)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
