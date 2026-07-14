//go:build integration

// Package repository — SessionRepository integration tests (active session
// list feature). These exercise session.go against a real PostgreSQL 16
// container via testcontainers, applying migrations up through
// 20260706000001 (the user_sessions migration). They share the gooseUpTo /
// containers.Postgres helpers defined in migrations_test.go (same package,
// same build tag).
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// setupSessionRepo spins up a fresh PostgreSQL 16 container, applies all
// migrations up to and including 20260706000001 (user_sessions), and returns a
// pool. Cleanup is registered via t.Cleanup.
func setupSessionRepo(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	// Fresh PostgreSQL 16 container; containers.Postgres registers its own
	// t.Cleanup for teardown.
	dsn := containers.Postgres(t)

	// Apply the schema through the user_sessions migration.
	gooseUpTo(t, dsn, "20260706000001")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func TestSessionRepository_lifecycle(t *testing.T) {
	ctx := context.Background()
	pool := setupSessionRepo(t, ctx) // use the existing helper found in migrations_test.go
	repo := NewSessionRepository(pool)

	userID, other := uuid.New(), uuid.New()
	tenantID := uuid.New()
	// mk inserts a live session for uid and returns its sid.
	mk := func(uid uuid.UUID) uuid.UUID {
		sid := uuid.New()
		if err := repo.Create(ctx, Session{
			SID: sid, UserID: uid, TenantID: tenantID,
			DeviceLabel: "Chrome on macOS", UserAgent: "ua", IP: "203.0.113.7",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return sid
	}
	s1, s2 := mk(userID), mk(userID)
	_ = mk(other)

	// idleCutoff an hour in the past: all freshly-created rows remain live.
	idleCutoff := time.Now().Add(-time.Hour)
	live, err := repo.ListLive(ctx, userID, idleCutoff)
	if err != nil || len(live) != 2 {
		t.Fatalf("ListLive: got %d err=%v, want 2", len(live), err)
	}

	// Cross-user revoke must not touch a session owned by someone else.
	if _, ok, _ := repo.RevokeOwned(ctx, other, s1); ok {
		t.Fatal("cross-user revoke must not succeed")
	}
	// Owner revoke of s1 succeeds.
	if _, ok, err := repo.RevokeOwned(ctx, userID, s1); err != nil || !ok {
		t.Fatalf("RevokeOwned: ok=%v err=%v", ok, err)
	}
	// After revoking s1, only s2 remains live.
	live, _ = repo.ListLive(ctx, userID, idleCutoff)
	if len(live) != 1 || live[0].SID != s2 {
		t.Fatalf("after revoke expected only s2, got %+v", live)
	}

	// RevokeOthers(keep=s2) is idempotent by design (SEC-081): it selects
	// EVERY non-kept, unexpired row — revoked or not — and COALESCEs
	// revoked_at, so a retry after a transient Redis-gate failure re-returns
	// the same set and re-drives every gate. s1 is already revoked but its
	// expires_at is still 24h in the future, so it is re-returned here.
	// s2 is kept, `other`'s session belongs to a different user. Expect s1.
	revoked, err := repo.RevokeOthers(ctx, userID, s2)
	if err != nil || len(revoked) != 1 || revoked[0].SID != s1 {
		t.Fatalf("RevokeOthers(keep=s2) should re-return the revoked-but-unexpired s1 (SEC-081 idempotency), got %+v err=%v", revoked, err)
	}
}
