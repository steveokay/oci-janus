//go:build integration

// Package repository — TestUserRepo_HumanGuards verifies the spec §4.1
// kind-guard contract: every …Human… method excludes kind='service_account'
// rows at the SQL layer, and GetUserAnyKind is the only path that can return
// a service-account shadow user.
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// newUserRepoWithMigrations boots a fresh PostgreSQL container via
// testcontainers, applies all FE-API-048 migrations (up to and including
// 20260622000003, the polymorphic api_keys migration), and returns a
// *UserRepository backed by a pgxpool.
//
// The container and pool are cleaned up automatically by t.Cleanup.
func newUserRepoWithMigrations(t *testing.T, ctx context.Context) *UserRepository {
	t.Helper()

	// Spin up a fresh PostgreSQL 16 container.
	dsn := containers.Postgres(t)

	// Apply the FULL current migration set — UserRepository.Create/scanOne read
	// the full current users column set (is_global_admin @ 20260629000001,
	// onboarding_complete @ 20260629000002, sso_subject @ 20260629222534/
	// 20260630120000, …). Any fixed pin goes stale the moment a later migration
	// touches the users columns Create references, re-introducing "column ... does
	// not exist" (FUT-085). gooseUp keeps the fixture aligned with the live schema
	// automatically. gooseUp is defined in migrations_test.go (same package + tag).
	gooseUp(t, dsn)

	// Build the pgxpool that the repository will use for queries.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return NewUserRepository(pool)
}

// idsOf extracts the ID field from a slice of User values so test assertions
// can use require.Contains / require.NotContains on the id list without
// iterating manually.
func idsOf(us []User) []uuid.UUID {
	out := make([]uuid.UUID, len(us))
	for i, u := range us {
		out[i] = u.ID
	}
	return out
}

// TestUserRepo_HumanGuards verifies the full set of kind-guard contracts
// mandated by spec §4.1:
//
//   - ListHumans excludes service-account shadow users
//   - GetHumanByEmail returns ErrNotFound for SA synthetic emails
//   - GetHumanByID returns ErrNotFound for SA shadow-user IDs
//   - CountHumans excludes SA rows from the headcount
//   - GetUserAnyKind successfully returns a service-account shadow user
func TestUserRepo_HumanGuards(t *testing.T) {
	ctx := context.Background()
	repo := newUserRepoWithMigrations(t, ctx)

	tenant := uuid.New()

	// Seed a human user — should appear in all Human* methods.
	human, err := repo.Create(ctx, CreateUserRequest{
		TenantID:     tenant,
		Username:     "human-guard",
		Email:        "h@example.com",
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err)

	// Seed a service-account shadow user — must be invisible to all Human* methods.
	// A non-empty password_hash is set deliberately so the GetHumanByUsername
	// case below proves the kind='human' SQL guard (SEC-075) is the control, not
	// the empty-hash argon2 barrier.
	sa, err := repo.Create(ctx, CreateUserRequest{
		TenantID:     tenant,
		Username:     "sa-guard",
		Email:        "sa+1@internal.invalid",
		PasswordHash: "argon2-shadow-hash-nonempty",
		Kind:         "service_account",
	})
	require.NoError(t, err)

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		// ListHumans must return the human row but exclude the SA shadow row.
		{"ListHumans excludes SA", func(t *testing.T) {
			users, err := repo.ListHumans(ctx, tenant, ListOpts{})
			require.NoError(t, err)
			ids := idsOf(users)
			require.Contains(t, ids, human.ID, "human user must appear in ListHumans")
			require.NotContains(t, ids, sa.ID, "SA shadow user must not appear in ListHumans")
		}},

		// GetHumanByEmail must return ErrNotFound for the SA's synthetic email
		// so that the SA cannot be loaded onto a human identity via email lookup
		// (e.g. SSO callback path).
		{"GetHumanByEmail rejects SA synthetic email", func(t *testing.T) {
			_, err := repo.GetHumanByEmail(ctx, tenant, "sa+1@internal.invalid")
			require.ErrorIs(t, err, ErrNotFound,
				"GetHumanByEmail must return ErrNotFound for SA synthetic email")
		}},

		// GetHumanByID must return ErrNotFound when the ID belongs to an SA
		// shadow user so that SA credentials cannot be loaded via a direct ID
		// lookup (e.g. JWT subject resolution path).
		{"GetHumanByID rejects SA shadow id", func(t *testing.T) {
			_, err := repo.GetHumanByID(ctx, sa.ID)
			require.ErrorIs(t, err, ErrNotFound,
				"GetHumanByID must return ErrNotFound for SA shadow-user ID")
		}},

		// GetHumanByUsername must return the human row but reject the SA shadow
		// row's synthetic username (SEC-075) so a service-account cannot
		// authenticate on the username/password login path — even though the SA
		// row above carries a non-empty password_hash.
		{"GetHumanByUsername matches human, rejects SA username", func(t *testing.T) {
			got, err := repo.GetHumanByUsername(ctx, tenant, "human-guard")
			require.NoError(t, err)
			require.Equal(t, human.ID, got.ID,
				"GetHumanByUsername must return the human user")

			_, err = repo.GetHumanByUsername(ctx, tenant, "sa-guard")
			require.ErrorIs(t, err, ErrNotFound,
				"GetHumanByUsername must return ErrNotFound for an SA shadow username")
		}},

		// CountHumans must only count human rows; adding an SA must not affect
		// the count so that billing / plan-limit checks use the real headcount.
		{"CountHumans excludes SA", func(t *testing.T) {
			n, err := repo.CountHumans(ctx, tenant)
			require.NoError(t, err)
			require.EqualValues(t, 1, n,
				"CountHumans must count only human users, not SA shadow rows")
		}},

		// GetUserAnyKind is the explicitly kind-agnostic path — it must be able
		// to return the SA shadow user so the SA management handlers can load it.
		{"GetUserAnyKind returns SA when asked", func(t *testing.T) {
			got, err := repo.GetUserAnyKind(ctx, sa.ID)
			require.NoError(t, err)
			require.Equal(t, "service_account", got.Kind,
				"GetUserAnyKind must return the SA shadow user with correct kind")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}
