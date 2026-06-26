// Package service_test contains tests that exercise the Service methods that
// interact with Redis. These tests use miniredis — an in-process Redis
// implementation — so no real Redis server is needed.
package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// setupService spins up a miniredis instance, generates a fresh RSA-2048 key pair,
// and returns a Service wired to use them. The returned cleanup func stops the
// miniredis server.
//
// We deliberately use real RSA key generation here (not static test keys) so that
// IssueToken and ValidateToken exercise the real cryptographic paths.
func setupService(t *testing.T) (*Service, func()) {
	t.Helper()

	// Start in-process Redis.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// Generate a fresh RSA-2048 key pair (not 1024 — we'll sign real tokens).
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	// Encode private key as PKCS8 PEM, then base64.
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	privB64 := base64.StdEncoding.EncodeToString(privPEM)

	// Encode public key as PKIX PEM, then base64.
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	pubB64 := base64.StdEncoding.EncodeToString(pubPEM)

	// Construct the Service. We pass nil repositories because the tests below
	// only exercise JWT and Redis paths — not DB paths.
	svc, err := New(nil, nil, nil, nil, rdb, privB64, pubB64, "kid-test")
	if err != nil {
		mr.Close()
		t.Fatalf("New service: %v", err)
	}

	return svc, func() {
		_ = rdb.Close()
		mr.Close()
	}
}

// TestIssueToken_validTokenCanBeValidated verifies the full sign → parse flow:
// IssueToken produces a token that ValidateToken accepts.
func TestIssueToken_validTokenCanBeValidated(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	token, err := svc.IssueToken(ctx, "user-1", "tenant-1", []RepositoryAccess{
		{Type: "repository", Name: "myorg/myimage", Actions: []string{"push", "pull"}},
	}, nil, false)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token == "" {
		t.Fatal("IssueToken returned empty token")
	}

	claims, err := svc.ValidateToken(ctx, token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("Subject: got %q, want %q", claims.Subject, "user-1")
	}
	if claims.TenantID != "tenant-1" {
		t.Errorf("TenantID: got %q, want %q", claims.TenantID, "tenant-1")
	}
	if len(claims.Access) != 1 || claims.Access[0].Name != "myorg/myimage" {
		t.Errorf("Access: unexpected value %+v", claims.Access)
	}
}

// TestValidateToken_invalidTokenRejected verifies that a malformed or randomly
// generated token is rejected with an error.
func TestValidateToken_invalidTokenRejected(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	_, err := svc.ValidateToken(context.Background(), "this.is.not.a.valid.jwt")
	if err == nil {
		t.Error("expected error for invalid token, got nil")
	}
}

// TestRevokeToken_revokedTokenIsRejected verifies that after RevokeToken the
// same token string is rejected by ValidateToken.
func TestRevokeToken_revokedTokenIsRejected(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()

	// Issue a fresh token.
	token, err := svc.IssueToken(ctx, "user-revoke", "tenant-1", nil, nil, false)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	// Validate it first to obtain the parsed claims.
	claims, err := svc.ValidateToken(ctx, token)
	if err != nil {
		t.Fatalf("ValidateToken before revocation: %v", err)
	}

	// Revoke the token using its claims.
	if err := svc.RevokeToken(ctx, claims); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	// The same token must now be rejected.
	_, err = svc.ValidateToken(ctx, token)
	if err == nil {
		t.Error("expected error for revoked token, got nil")
	}
}

// TestIssueToken_jwtHeader verifies that the kid header is set correctly on the
// issued token so the JWKS endpoint can be used for key selection.
func TestIssueToken_jwtHeader(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := svc.IssueToken(ctx, "user-1", "tenant-1", nil, nil, false)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	// The key ID used in setupService is "kid-test"; verify JWKS reflects it.
	jwks := svc.JWKS()
	if len(jwks.Keys) != 1 {
		t.Fatalf("expected 1 JWK, got %d", len(jwks.Keys))
	}
	if jwks.Keys[0].Kid != "kid-test" {
		t.Errorf("JWK kid: got %q, want %q", jwks.Keys[0].Kid, "kid-test")
	}
}

// TestCheckIPRateLimit_belowThreshold verifies that an IP with no recorded
// failures is allowed through.
func TestCheckIPRateLimit_belowThreshold(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	if err := svc.CheckIPRateLimit(ctx, "1.2.3.4"); err != nil {
		t.Errorf("expected nil for fresh IP, got: %v", err)
	}
}

// TestCheckIPRateLimit_exceedThreshold verifies that an IP that has been
// recorded failing 10+ times is rate-limited.
func TestCheckIPRateLimit_exceedThreshold(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	const ip = "5.6.7.8"

	// Record 10 failures to trip the limit.
	for i := 0; i < 10; i++ {
		svc.RecordAuthFailure(ctx, ip)
	}

	if err := svc.CheckIPRateLimit(ctx, ip); err == nil {
		t.Error("expected rate-limit error after 10 failures, got nil")
	}
}
