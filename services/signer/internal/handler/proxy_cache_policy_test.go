// Package handler tests for the FUT-017 proxy-cache auto-sign policy RPCs.
// Uses bufconn to exercise the real production handler over a gRPC channel
// so the streaming ListProxyCacheSignPolicies endpoint runs through the
// generated server/client glue rather than being bypassed.
package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// ── In-memory policy repo fake ───────────────────────────────────────────────

// memPolicyRepo satisfies ProxyCachePolicyRepo with a hand-rolled in-memory
// map. Production code uses a *repository.Repository (real PG); these tests
// just need a stub that records writes + serves reads consistently.
type memPolicyRepo struct {
	mu       sync.Mutex
	rows     map[string]*repository.ProxyCacheSignPolicy // keyed by tenant|upstream
	getErr   error
	upErr    error
	listErr  error
}

func newMemPolicyRepo() *memPolicyRepo {
	return &memPolicyRepo{rows: map[string]*repository.ProxyCacheSignPolicy{}}
}

func memKey(tenantID, upstreamName string) string { return tenantID + "|" + upstreamName }

func (m *memPolicyRepo) GetProxyCacheSignPolicy(_ context.Context, tenantID, upstreamName string) (*repository.ProxyCacheSignPolicy, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[memKey(tenantID, upstreamName)], nil
}

func (m *memPolicyRepo) UpsertProxyCacheSignPolicy(_ context.Context, p *repository.ProxyCacheSignPolicy) (*repository.ProxyCacheSignPolicy, error) {
	if m.upErr != nil {
		return nil, m.upErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	key := memKey(p.TenantID, p.UpstreamName)
	existing, ok := m.rows[key]
	stored := *p
	if ok {
		stored.CreatedAt = existing.CreatedAt
	} else {
		stored.CreatedAt = now
	}
	stored.UpdatedAt = now
	m.rows[key] = &stored
	return &stored, nil
}

func (m *memPolicyRepo) ListProxyCacheSignPolicies(_ context.Context, tenantID string) ([]*repository.ProxyCacheSignPolicy, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*repository.ProxyCacheSignPolicy
	for _, p := range m.rows {
		if p.TenantID == tenantID {
			c := *p
			out = append(out, &c)
		}
	}
	return out, nil
}

// ── bufconn harness ──────────────────────────────────────────────────────────

// startBufconnServer brings up a tiny in-process gRPC server registered with
// the real production GRPCHandler. The returned client + cleanup cover the
// streaming + unary RPC tests below.
func startBufconnServer(t *testing.T, repo ProxyCachePolicyRepo) (signerv1.SignerServiceClient, func()) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()

	// A nil signer is fine here — none of the FUT-017 policy RPCs touch the
	// signer. The store is also nil-safe for these RPCs.
	hdl := New(nil, sigstore.New()).WithProxyCachePolicyRepo(repo)
	signerv1.RegisterSignerServiceServer(srv, hdl)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn serve: %v", err)
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return signerv1.NewSignerServiceClient(conn), cleanup
}

// ── Get tests ────────────────────────────────────────────────────────────────

func TestGetProxyCacheSignPolicy_AbsentReturnsZeroValuedRow(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	got, err := client.GetProxyCacheSignPolicy(context.Background(), &signerv1.GetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Absent row -> zero-valued response, NOT a NOT_FOUND error.
	if got.AutoSign {
		t.Errorf("expected auto_sign=false for missing row, got true")
	}
	if got.KeyId != "" {
		t.Errorf("expected empty key_id for missing row, got %q", got.KeyId)
	}
	if got.TenantId != "tenant-1" || got.UpstreamName != "dockerhub" {
		t.Errorf("expected request fields to be echoed, got tenant=%q upstream=%q", got.TenantId, got.UpstreamName)
	}
}

func TestGetProxyCacheSignPolicy_MissingTenant_InvalidArgument(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	_, err := client.GetProxyCacheSignPolicy(context.Background(), &signerv1.GetProxyCacheSignPolicyRequest{
		UpstreamName: "dockerhub",
	})
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", code, err)
	}
}

// ── Set tests ────────────────────────────────────────────────────────────────

func TestSetProxyCacheSignPolicy_Insert(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	got, err := client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
		AutoSign:     true,
		KeyId:        "key-1",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !got.AutoSign || got.KeyId != "key-1" {
		t.Errorf("response did not echo Set fields: %+v", got)
	}

	// The stored row should round-trip through Get.
	round, err := client.GetProxyCacheSignPolicy(context.Background(), &signerv1.GetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
	})
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !round.AutoSign || round.KeyId != "key-1" {
		t.Errorf("round-trip mismatch: %+v", round)
	}
}

func TestSetProxyCacheSignPolicy_Upsert(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	if _, err := client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
		AutoSign:     true,
		KeyId:        "key-1",
	}); err != nil {
		t.Fatalf("Set #1: %v", err)
	}
	// Flip the flag.
	got, err := client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
		AutoSign:     false,
		KeyId:        "key-2",
	})
	if err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	if got.AutoSign || got.KeyId != "key-2" {
		t.Errorf("upsert did not overwrite: %+v", got)
	}
}

func TestSetProxyCacheSignPolicy_RepoError_Internal(t *testing.T) {
	repo := newMemPolicyRepo()
	repo.upErr = errors.New("db boom")
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	_, err := client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId:     "tenant-1",
		UpstreamName: "dockerhub",
		AutoSign:     true,
		KeyId:        "key-1",
	})
	if code := status.Code(err); code != codes.Internal {
		t.Fatalf("expected Internal, got %v (%v)", code, err)
	}
}

// ── List tests ───────────────────────────────────────────────────────────────

func TestListProxyCacheSignPolicies_Streams(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	// Seed three rows for tenant-1, one for tenant-2.
	for _, p := range []struct {
		tenant, upstream, key string
		auto                  bool
	}{
		{"tenant-1", "dockerhub", "k1", true},
		{"tenant-1", "ghcr", "k2", true},
		{"tenant-1", "quay", "", false},
		{"tenant-2", "dockerhub", "kx", true},
	} {
		if _, err := client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
			TenantId:     p.tenant,
			UpstreamName: p.upstream,
			AutoSign:     p.auto,
			KeyId:        p.key,
		}); err != nil {
			t.Fatalf("seed Set(%s,%s): %v", p.tenant, p.upstream, err)
		}
	}

	stream, err := client.ListProxyCacheSignPolicies(context.Background(), &signerv1.ListProxyCacheSignPoliciesRequest{
		TenantId: "tenant-1",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]bool{}
	for {
		row, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if row.TenantId != "tenant-1" {
			t.Errorf("got cross-tenant row: %+v", row)
		}
		seen[row.UpstreamName] = true
	}
	for _, want := range []string{"dockerhub", "ghcr", "quay"} {
		if !seen[want] {
			t.Errorf("expected %q in stream, got %v", want, seen)
		}
	}
	if len(seen) != 3 {
		t.Errorf("expected exactly 3 rows for tenant-1, got %d (%v)", len(seen), seen)
	}
}

func TestListProxyCacheSignPolicies_MissingTenant_InvalidArgument(t *testing.T) {
	repo := newMemPolicyRepo()
	client, cleanup := startBufconnServer(t, repo)
	defer cleanup()

	stream, err := client.ListProxyCacheSignPolicies(context.Background(), &signerv1.ListProxyCacheSignPoliciesRequest{})
	if err != nil {
		t.Fatalf("List call: %v", err)
	}
	_, err = stream.Recv()
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", code, err)
	}
}

// ── Repository unwired branch ────────────────────────────────────────────────

func TestPolicyRPCs_NoRepo_FailedPrecondition(t *testing.T) {
	// Skip the helper — we want a handler with NO policy repo.
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	signerv1.RegisterSignerServiceServer(srv, New(nil, sigstore.New()))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	client := signerv1.NewSignerServiceClient(conn)

	_, err = client.GetProxyCacheSignPolicy(context.Background(), &signerv1.GetProxyCacheSignPolicyRequest{
		TenantId: "t", UpstreamName: "u",
	})
	if code := status.Code(err); code != codes.FailedPrecondition {
		t.Errorf("Get without repo: expected FailedPrecondition, got %v", code)
	}

	_, err = client.SetProxyCacheSignPolicy(context.Background(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId: "t", UpstreamName: "u",
	})
	if code := status.Code(err); code != codes.FailedPrecondition {
		t.Errorf("Set without repo: expected FailedPrecondition, got %v", code)
	}
}
