//go:build integration

// Package repository — MFA repository integration tests (Task 3, TOTP MFA).
//
// These exercise mfa.go against a real PostgreSQL 16 container via
// testcontainers, applying migrations up through 20260705120000 (the Task 1
// users_mfa migration that adds the mfa_* columns and the
// user_mfa_backup_codes table). They share the gooseUpTo / containers.Postgres
// helpers defined in migrations_test.go (same package, same build tag).
package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// setupMFARepo spins up a fresh PostgreSQL 16 container, applies all migrations
// up to and including 20260705120000 (users_mfa), and returns a pool plus a
// UserRepository sharing it. Cleanup is registered via t.Cleanup.
func setupMFARepo(t *testing.T, ctx context.Context) (*pgxpool.Pool, *UserRepository) {
	t.Helper()

	// Fresh PostgreSQL 16 container; containers.Postgres registers its own
	// t.Cleanup for teardown.
	dsn := containers.Postgres(t)

	// Apply the schema through the Task 1 MFA migration.
	gooseUpTo(t, dsn, "20260705120000")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool, NewUserRepository(pool)
}

// seedHumanForMFA inserts a human user in a fresh tenant and returns its id.
func seedHumanForMFA(t *testing.T, ctx context.Context, users *UserRepository) uuid.UUID {
	t.Helper()

	u, err := users.Create(ctx, CreateUserRequest{
		TenantID:     uuid.New(),
		Username:     "mfa-" + uuid.New().String()[:8],
		Email:        uuid.New().String()[:8] + "@example.com",
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err, "seedHumanForMFA: Create")
	return u.ID
}

// TestMFAState_Lifecycle walks a user through the full MFA state machine:
// disabled → pending secret → enabled → counter advance → disabled.
func TestMFAState_Lifecycle(t *testing.T) {
	ctx := context.Background()
	_, users := setupMFARepo(t, ctx)

	userID := seedHumanForMFA(t, ctx, users)

	// ── Fresh user: MFA disabled, no secret, all nullable state nil. ──────────
	st, err := users.GetMFAState(ctx, userID)
	require.NoError(t, err)
	require.False(t, st.Enabled, "fresh user must have MFA disabled")
	require.Nil(t, st.SecretEnc, "fresh user must have no secret")
	require.Nil(t, st.SecretKEKVersion, "fresh user must have no kek version")
	require.Nil(t, st.EnrolledAt, "fresh user must have no enrolled_at")
	require.Nil(t, st.LastUsedCounter, "fresh user must have no counter")

	// Unknown user id → ErrNotFound.
	_, err = users.GetMFAState(ctx, uuid.New())
	require.True(t, errors.Is(err, ErrNotFound), "unknown id must map to ErrNotFound, got %v", err)

	// ── SetPendingMFASecret: secret + kek stored, still disabled. ─────────────
	secret := []byte{0xde, 0xad, 0xbe, 0xef}
	require.NoError(t, users.SetPendingMFASecret(ctx, userID, secret, 7))

	st, err = users.GetMFAState(ctx, userID)
	require.NoError(t, err)
	require.False(t, st.Enabled, "pending secret must not enable MFA")
	require.Equal(t, secret, st.SecretEnc, "secret ciphertext must round-trip")
	require.NotNil(t, st.SecretKEKVersion)
	require.Equal(t, int16(7), *st.SecretKEKVersion, "kek version must round-trip")
	require.Nil(t, st.EnrolledAt, "enrolled_at must remain nil until EnableMFA")

	// ── EnableMFA: flag flips, enrolled_at stamped. ───────────────────────────
	require.NoError(t, users.EnableMFA(ctx, userID))

	st, err = users.GetMFAState(ctx, userID)
	require.NoError(t, err)
	require.True(t, st.Enabled, "EnableMFA must set mfa_enabled")
	require.NotNil(t, st.EnrolledAt, "EnableMFA must stamp enrolled_at")
	require.Equal(t, secret, st.SecretEnc, "secret must survive enable")

	// ── AdvanceMFACounter: atomic compare-and-swap (SEC-078). ─────────────────
	advanced, err := users.AdvanceMFACounter(ctx, userID, 42)
	require.NoError(t, err)
	require.True(t, advanced, "first advance must win the CAS")

	st, err = users.GetMFAState(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, st.LastUsedCounter)
	require.Equal(t, int64(42), *st.LastUsedCounter, "counter must round-trip")

	// A second advance at the same (or a lower) counter loses the CAS: this is
	// the guarantee that two concurrent requests carrying the same OTP cannot
	// both be accepted — one OTP, one token.
	replayed, err := users.AdvanceMFACounter(ctx, userID, 42)
	require.NoError(t, err)
	require.False(t, replayed, "re-advancing at the same counter must lose the CAS")

	// ── DisableMFA: all MFA state cleared. ────────────────────────────────────
	require.NoError(t, users.DisableMFA(ctx, userID))

	st, err = users.GetMFAState(ctx, userID)
	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.Nil(t, st.SecretEnc)
	require.Nil(t, st.SecretKEKVersion)
	require.Nil(t, st.EnrolledAt)
	require.Nil(t, st.LastUsedCounter)

	// Mutators against an unknown id map to ErrNotFound (RowsAffected == 0).
	require.True(t, errors.Is(users.EnableMFA(ctx, uuid.New()), ErrNotFound),
		"EnableMFA on unknown id must map to ErrNotFound")
}

// TestBackupCodes_InsertConsumeSingleUse covers insert, single-use consumption,
// the double-spend guard, and replace-on-reinsert semantics.
func TestBackupCodes_InsertConsumeSingleUse(t *testing.T) {
	ctx := context.Background()
	_, users := setupMFARepo(t, ctx)

	userID := seedHumanForMFA(t, ctx, users)

	// ── Insert 3 codes → 3 unused. ────────────────────────────────────────────
	require.NoError(t, users.InsertBackupCodes(ctx, userID, []string{"h1", "h2", "h3"}))

	codes, err := users.ListUnusedBackupCodes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, codes, 3, "three unused codes expected after insert")

	// ── Consume the first code → OK. ──────────────────────────────────────────
	first := codes[0].ID
	require.NoError(t, users.MarkBackupCodeUsed(ctx, first))

	// ── Second consume of the same code → ErrNotFound (single-use guard). ─────
	err = users.MarkBackupCodeUsed(ctx, first)
	require.True(t, errors.Is(err, ErrNotFound),
		"re-using a spent backup code must map to ErrNotFound, got %v", err)

	// ── List now shows 2 unused. ──────────────────────────────────────────────
	codes, err = users.ListUnusedBackupCodes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, codes, 2, "one code consumed leaves two unused")

	// ── Re-insert 2 codes → replaces all prior (including the spent one). ─────
	require.NoError(t, users.InsertBackupCodes(ctx, userID, []string{"n1", "n2"}))

	codes, err = users.ListUnusedBackupCodes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, codes, 2, "reinsert must replace the code set, leaving exactly two unused")

	// ── DeleteBackupCodes clears the set. ─────────────────────────────────────
	require.NoError(t, users.DeleteBackupCodes(ctx, userID))
	codes, err = users.ListUnusedBackupCodes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, codes, 0, "delete must remove all backup codes")
}
