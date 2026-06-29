// Package server tests cover the Phase 3.4 bootstrap-tenant-id fetch path.
// The dial half of fetchBootstrapTenantID is exercised by other integration
// tests (it shares buildClientCreds with the audit client); here we focus on
// the RPC + parse seam via the getBootstrapTenantIDFromClient helper, which
// takes a pre-built TenantServiceClient so a bufconn-backed in-process server
// can stand in for registry-tenant.
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
// GetDeploymentMetadata path. Each test case sets the value/err knobs that the
// embedded UnimplementedTenantServiceServer leaves untouched on every other
// RPC so an accidental extra call (none expected) returns Unimplemented rather
// than a panic.
type fakeTenantServer struct {
	tenantv1.UnimplementedTenantServiceServer

	// value is returned verbatim when err == nil. For bootstrap_tenant_id the
	// production schema stores a JSON-encoded UUID string (e.g. `"<uuid>"`).
	value []byte
	// err is returned instead of value when non-nil. Use status.Error to mimic
	// the production NotFound branch.
	err error
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
// fake, returning a client wired to it. Mirrors startAuditBufconn in
// internal/service/activity_integration_test.go so the test wiring is
// consistent across the codebase.
func startTenantBufconn(t *testing.T, fake *fakeTenantServer) tenantv1.TenantServiceClient {
	t.Helper()

	const bufSize = 1024 * 1024 // 1 MiB in-memory buffer, same as audit tests
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	tenantv1.RegisterTenantServiceServer(srv, fake)

	// Serve in the background; errors after GracefulStop are expected.
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

// TestGetBootstrapTenantIDFromClient_HappyPath verifies the canonical
// JSON-string-encoded UUID shape that the bootstrap CLI writes into
// deployment_metadata is parsed back to a bare UUID string.
func TestGetBootstrapTenantIDFromClient_HappyPath(t *testing.T) {
	tenantID := uuid.New()
	// Mirror what services/tenant.SetDeploymentMetadata stores — JSONB
	// encoding of the UUID string is a quoted string.
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

// TestGetBootstrapTenantIDFromClient_NotFound — the tenant service returns
// NotFound when deployment_metadata hasn't been seeded. Auth must surface
// this as a startup error so the operator sees "not bootstrapped" rather
// than the service silently starting without the defence-in-depth interceptor.
func TestGetBootstrapTenantIDFromClient_NotFound(t *testing.T) {
	client := startTenantBufconn(t, &fakeTenantServer{
		err: status.Error(codes.NotFound, "deployment_metadata key not found"),
	})

	_, err := getBootstrapTenantIDFromClient(context.Background(), client)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// We don't assert the exact wording — only that the underlying gRPC status
	// is preserved through the fmt.Errorf wrap so operators can grep logs.
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound code, got %v (err=%v)", status.Code(err), err)
	}
}

// TestGetBootstrapTenantIDFromClient_NonJSONValue covers the data-corruption
// branch — schema guarantees JSONB but a forced-write bypass could land a
// raw byte slice. Validate this fails loudly instead of being passed to
// SingleTenantInjector as garbage.
func TestGetBootstrapTenantIDFromClient_NonJSONValue(t *testing.T) {
	client := startTenantBufconn(t, &fakeTenantServer{
		value: []byte("not-json-at-all"),
	})

	_, err := getBootstrapTenantIDFromClient(context.Background(), client)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Assert the wrap mentions the parse step so a log line is actionable.
	// Earlier draft used `errors.Is(err, err)` here — a tautological
	// self-comparison that staticcheck SA4000 flags + that didn't actually
	// verify the wrap chain. Substring check on the wrap message is what we
	// want: if production stops wrapping with "parse bootstrap_tenant_id JSON"
	// the test breaks.
	if !strings.Contains(err.Error(), "parse bootstrap_tenant_id JSON") {
		t.Errorf("error should mention parse step; got: %v", err)
	}
}

// TestGetBootstrapTenantIDFromClient_NotAUUID covers the case where the JSON
// parses cleanly but the inner string isn't a UUID. UUID validation here means
// SingleTenantInjector can trust its input without re-parsing.
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
