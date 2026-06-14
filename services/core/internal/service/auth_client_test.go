// Package service (auth_client_test.go) exercises AuthClient.ValidateBearer
// and ValidateAPIKey using an in-process fake gRPC auth server (bufconn).
// No build tags — runs with plain `go test ./...`.
package service

import (
	"context"
	"net"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// ── fake auth gRPC server ─────────────────────────────────────────────────────

// fakeAuthServer is a minimal in-process AuthService that accepts any non-empty
// token and rejects empty ones, and validates API keys by a hard-coded table.
type fakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
	// validAPIKeys maps keyID → rawSecret for ValidateAPIKey tests.
	validAPIKeys map[string]string
}

func newFakeAuthServer() *fakeAuthServer {
	return &fakeAuthServer{
		validAPIKeys: make(map[string]string),
	}
}

func (s *fakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	if req.GetToken() == "" || req.GetToken() == "invalid-token" {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	return &authv1.ValidateTokenResponse{
		Valid:     true,
		UserId:    "user-123",
		TenantId:  "tenant-456",
		Jti:       "jti-789",
		Access:    []*authv1.RepositoryAccess{{Type: "repository", Name: "*", Actions: []string{"push", "pull"}}},
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
	}, nil
}

func (s *fakeAuthServer) ValidateAPIKey(_ context.Context, req *authv1.ValidateAPIKeyRequest) (*authv1.ValidateAPIKeyResponse, error) {
	secret, ok := s.validAPIKeys[req.GetKeyId()]
	if !ok || secret != req.GetRawSecret() {
		return nil, status.Error(codes.Unauthenticated, "invalid api key")
	}
	return &authv1.ValidateAPIKeyResponse{
		Valid:    true,
		UserId:   "robot-user",
		TenantId: "tenant-robot",
	}, nil
}

// ── test helper ───────────────────────────────────────────────────────────────

// buildAuthClient creates an AuthClient backed by the fake auth server and
// miniredis. Returns the client, the fake server (for state setup), and cleanup.
func buildAuthClient(t *testing.T) (*AuthClient, *fakeAuthServer, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	fake := newFakeAuthServer()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	authv1.RegisterAuthServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		mr.Close()
		t.Fatalf("grpc.NewClient: %v", err)
	}

	client := NewAuthClient(conn, rdb)

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = rdb.Close()
		mr.Close()
	}
	return client, fake, cleanup
}

// ── ValidateBearer ────────────────────────────────────────────────────────────

// TestValidateBearer_validToken_returnsClaims verifies that a valid token is
// accepted and the response claims are correctly populated.
func TestValidateBearer_validToken_returnsClaims(t *testing.T) {
	client, _, cleanup := buildAuthClient(t)
	defer cleanup()

	claims, err := client.ValidateBearer(context.Background(), "valid-token-string")
	if err != nil {
		t.Fatalf("ValidateBearer: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID: got %q, want user-123", claims.UserID)
	}
	if claims.TenantID != "tenant-456" {
		t.Errorf("TenantID: got %q, want tenant-456", claims.TenantID)
	}
	if len(claims.Access) == 0 {
		t.Error("expected non-empty Access claims")
	}
}

// TestValidateBearer_invalidToken_returnsErrUnauthorized verifies that an
// invalid token is rejected with ErrUnauthorized.
func TestValidateBearer_invalidToken_returnsErrUnauthorized(t *testing.T) {
	client, _, cleanup := buildAuthClient(t)
	defer cleanup()

	_, err := client.ValidateBearer(context.Background(), "invalid-token")
	if err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

// TestValidateBearer_cachedResult_skipsgRPC verifies that a second call with
// the same token is served from the Redis cache (the fake server will still
// respond the same way — this test just ensures no error on cache hit).
func TestValidateBearer_cachedResult_returnsClaimsFromCache(t *testing.T) {
	client, _, cleanup := buildAuthClient(t)
	defer cleanup()

	ctx := context.Background()
	const tok = "cached-token-abc"

	// First call — goes to gRPC and caches the result.
	claims1, err := client.ValidateBearer(ctx, tok)
	if err != nil {
		t.Fatalf("first ValidateBearer: %v", err)
	}

	// Second call — should be served from Redis cache.
	claims2, err := client.ValidateBearer(ctx, tok)
	if err != nil {
		t.Fatalf("second ValidateBearer: %v", err)
	}
	if claims1.UserID != claims2.UserID {
		t.Errorf("cached UserID differs: %q vs %q", claims1.UserID, claims2.UserID)
	}
}

// ── ValidateAPIKey ────────────────────────────────────────────────────────────

// TestValidateAPIKey_validKey_returnsClaims verifies that a valid API key
// (keyID:secret) is accepted and returns claims.
func TestValidateAPIKey_validKey_returnsClaims(t *testing.T) {
	client, fake, cleanup := buildAuthClient(t)
	defer cleanup()

	// Register a valid key on the fake server.
	fake.validAPIKeys["key-uuid-1234"] = "supersecret"

	claims, err := client.ValidateAPIKey(context.Background(), "key-uuid-1234", "supersecret")
	if err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	if claims.UserID != "robot-user" {
		t.Errorf("UserID: got %q, want robot-user", claims.UserID)
	}
	if claims.TenantID != "tenant-robot" {
		t.Errorf("TenantID: got %q, want tenant-robot", claims.TenantID)
	}
}

// TestValidateAPIKey_invalidSecret_returnsError verifies that a wrong secret
// results in an error.
func TestValidateAPIKey_invalidSecret_returnsError(t *testing.T) {
	client, fake, cleanup := buildAuthClient(t)
	defer cleanup()

	fake.validAPIKeys["key-uuid-9999"] = "correctsecret"

	_, err := client.ValidateAPIKey(context.Background(), "key-uuid-9999", "wrongsecret")
	if err == nil {
		t.Error("expected error for wrong secret, got nil")
	}
}

// TestValidateAPIKey_unknownKey_returnsError verifies that an unrecognised key
// ID results in an error.
func TestValidateAPIKey_unknownKey_returnsError(t *testing.T) {
	client, _, cleanup := buildAuthClient(t)
	defer cleanup()

	_, err := client.ValidateAPIKey(context.Background(), "does-not-exist", "anysecret")
	if err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}
