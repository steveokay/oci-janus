// Package server tests cover the Phase 3.4 bootstrap-tenant-id fetch path
// for services/metadata. Mirrors the test shape used in
// services/auth/internal/server/bootstrap_tenant_id_test.go (PR #162) so a
// reviewer comparing the two services sees an identical contract.
package server

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
)

// fakeTenantServer is the minimal TenantServiceServer needed to drive the
// GetDeploymentMetadata path. value/err knobs override per test case; the
// embedded UnimplementedTenantServiceServer makes any other RPC return
// Unimplemented rather than panic.
type fakeTenantServer struct {
	tenantv1.UnimplementedTenantServiceServer
	value []byte
	err   error
}

func (f *fakeTenantServer) GetDeploymentMetadata(
	_ context.Context, _ *tenantv1.GetDeploymentMetadataRequest,
) (*tenantv1.GetDeploymentMetadataResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &tenantv1.GetDeploymentMetadataResponse{Value: f.value}, nil
}

// startTenantBufconn spins up an in-process gRPC server backed by the supplied
// fake, returning a client wired to it. Identical pattern to services/auth so
// future per-service follow-ups can copy-paste with confidence.
func startTenantBufconn(t *testing.T, fake *fakeTenantServer) tenantv1.TenantServiceClient {
	t.Helper()

	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	tenantv1.RegisterTenantServiceServer(srv, fake)

	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("startTenantBufconn: grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		srv.GracefulStop()
		_ = conn.Close()
		_ = lis.Close()
	})

	return tenantv1.NewTenantServiceClient(conn)
}

// TestGetBootstrapTenantIDFromClient_HappyPath — production schema stores a
// JSON-encoded UUID string. Verify it's parsed back to a bare UUID string.
func TestGetBootstrapTenantIDFromClient_HappyPath(t *testing.T) {
	tenantID := uuid.New()
	value, err := json.Marshal(tenantID.String())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	client := startTenantBufconn(t, &fakeTenantServer{value: value})

	got, err := getBootstrapTenantIDFromClient(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != tenantID.String() {
		t.Errorf("got %q, want %q", got, tenantID.String())
	}
}

// TestGetBootstrapTenantIDFromClient_NotFound — tenant returns NotFound when
// deployment_metadata hasn't been seeded. Metadata must surface this as a
// startup error so the operator runs the bootstrap CLI before retrying.
func TestGetBootstrapTenantIDFromClient_NotFound(t *testing.T) {
	client := startTenantBufconn(t, &fakeTenantServer{
		err: status.Error(codes.NotFound, "deployment_metadata key not found"),
	})

	_, err := getBootstrapTenantIDFromClient(context.Background(), client)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound code, got %v (err=%v)", status.Code(err), err)
	}
}

// TestGetBootstrapTenantIDFromClient_NonJSONValue — covers the data-corruption
// branch. Substring check (not errors.Is self-comparison) so the assertion
// breaks if production stops wrapping with this exact phrase.
func TestGetBootstrapTenantIDFromClient_NonJSONValue(t *testing.T) {
	client := startTenantBufconn(t, &fakeTenantServer{
		value: []byte("not-json-at-all"),
	})

	_, err := getBootstrapTenantIDFromClient(context.Background(), client)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse bootstrap_tenant_id JSON") {
		t.Errorf("error should mention parse step; got: %v", err)
	}
}

// TestGetBootstrapTenantIDFromClient_NotAUUID — JSON parses cleanly but the
// inner string isn't a UUID. UUID validation here means SingleTenantInjector
// can trust its input without re-parsing.
func TestGetBootstrapTenantIDFromClient_NotAUUID(t *testing.T) {
	value, err := json.Marshal("definitely-not-a-uuid")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	client := startTenantBufconn(t, &fakeTenantServer{value: value})

	_, err = getBootstrapTenantIDFromClient(context.Background(), client)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
