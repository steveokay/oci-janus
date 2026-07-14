//go:build integration

// Package repository — TestUserRepo_MarkOnboardingComplete verifies the
// REDESIGN-001 Phase 4.3 contract for users.onboarding_complete:
//   - the column starts at false on freshly-created humans;
//   - MarkOnboardingComplete flips it to true and returns the refreshed row;
//   - calling it a second time is idempotent (still returns the row, no error);
//   - calling it for a non-existent user returns ErrNotFound.
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// TestMarkOnboardingComplete_HappyPath_Idempotent exercises the full
// onboarding-flip lifecycle through the repository layer. Inline setup
// (rather than newUserRepoWithMigrations) is used because that helper stops
// at version 20260622000003 — we need the schema advanced to the migration
// under test (20260629000002) so the column actually exists.
//
// Why a single test (rather than four) — the lifecycle steps are sequential
// and share fixtures (one tenant, one user), so combining them keeps the
// container startup cost in line with similar fixtures in this package
// (e.g. TestUserRepo_HumanGuards).
func TestMarkOnboardingComplete_HappyPath_Idempotent(t *testing.T) {
	ctx := context.Background()

	// Spin up a fresh PostgreSQL 16 container and apply the FULL current migration
	// set. This test seeds via the live UserRepository.Create, whose RETURNING
	// clause reads sso_subject (added @ 20260629222534) — later than the old
	// 20260629000002 pin, so pinning left the column absent and Create failed
	// (FUT-085). gooseUp is defined in migrations_test.go.
	dsn := containers.Postgres(t)
	gooseUp(t, dsn)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repo := NewUserRepository(pool)

	tenant := uuid.New()

	// Seed a fresh human. The DEFAULT false on users.onboarding_complete means
	// a brand-new user must come back with OnboardingComplete=false — that's
	// the precondition for the wizard ever firing.
	u, err := repo.Create(ctx, CreateUserRequest{
		TenantID:     tenant,
		Username:     "onboard-user",
		Email:        "ob@example.com",
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err)
	require.False(t, u.OnboardingComplete, "fresh human must start with onboarding_complete=false")

	// First flip — should return the refreshed row with the flag set.
	got, err := repo.MarkOnboardingComplete(ctx, u.ID)
	require.NoError(t, err, "first MarkOnboardingComplete must succeed")
	require.True(t, got.OnboardingComplete, "row returned by MarkOnboardingComplete must have flag=true")
	require.Equal(t, u.ID, got.ID, "row returned must be the same user")

	// Confirm the flip persisted by re-reading the row independently.
	reread, err := repo.GetUserAnyKind(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, reread.OnboardingComplete, "persisted row must reflect onboarding_complete=true")

	// Idempotent — second call must not error. This is the contract the
	// wizard's "Done" button depends on for retries / double-clicks.
	again, err := repo.MarkOnboardingComplete(ctx, u.ID)
	require.NoError(t, err, "second MarkOnboardingComplete must be idempotent (no error)")
	require.True(t, again.OnboardingComplete)

	// Missing user — must map to ErrNotFound so the HTTP handler can return
	// 401 (vanished JWT subject) rather than 500.
	_, err = repo.MarkOnboardingComplete(ctx, uuid.New())
	require.ErrorIs(t, err, ErrNotFound, "missing user must map to ErrNotFound")
}
