package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// newTokenTestService builds a Service backed by an in-memory single-key RSA
// ring plus the caller-supplied miniredis client. It mirrors
// newSigningTestService but takes the *redis.Client as a parameter so the test
// controls the Redis lifecycle (needed by the session-token suite which asserts
// issue → validate → refresh round-trips against a live revocation store).
func newTokenTestService(t *testing.T, rdb redisClient) *Service {
	t.Helper()
	// A single deterministic RSA key is enough: these tests exercise the
	// issue/validate/refresh claim-plumbing, not key rotation.
	priv, _ := genTestKey(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "kid-test", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-test")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	svc, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ring)
	if err != nil {
		t.Fatalf("NewWithFakesAndRing: %v", err)
	}
	return svc
}

// TestIssueToken_carriesSid asserts the sid param lands in the Sid claim and
// that RefreshToken preserves it verbatim.
func TestIssueToken_carriesSid(t *testing.T) {
	rdb := newTestRedis(t)
	svc := newTokenTestService(t, rdb)
	ctx := context.Background()

	uuidNil := uuid.Nil.String()
	tok, err := svc.IssueToken(ctx, uuidNil, uuidNil, nil, nil, false, "human", []string{"pwd"}, "sess-123")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := svc.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Sid != "sess-123" {
		t.Fatalf("Sid: got %q want sess-123", claims.Sid)
	}

	refreshed, err := svc.RefreshToken(ctx, tok)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	rc, _ := svc.ValidateToken(ctx, refreshed)
	if rc.Sid != "sess-123" {
		t.Fatalf("refreshed Sid: got %q want sess-123 (must be preserved)", rc.Sid)
	}
}
