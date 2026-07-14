//go:build integration

// Package repository — TestAPIKeyRepo_Polymorphic covers the polymorphic
// ownership contract added in FE-API-048 Task 6:
//
//   - Create with UserID set (human-owned key) succeeds and returns a row with
//     UserID non-nil and ServiceAccountID nil.
//   - Create with ServiceAccountID set (SA-owned key) succeeds and returns a row
//     with ServiceAccountID non-nil and UserID nil.
//   - Create with both UserID and ServiceAccountID set is rejected at the
//     application layer (defence-in-depth) and also by the database CHECK
//     constraint api_keys_owner_exactly_one.
//   - Create with neither set is rejected at the application layer and also by
//     the CHECK constraint.
//   - GetByID returns the correct ServiceAccountID for SA-owned keys and the
//     correct UserID for human-owned keys.
//   - ListByUser returns human-owned keys and excludes SA-owned keys.
//   - ListByServiceAccount returns SA-owned keys and excludes human-owned keys.
//
// All tests run against a real PostgreSQL 16 container via testcontainers.
// They share the gooseUpTo / containers.Postgres helpers defined in
// migrations_test.go (same package, same build tag).
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// setupAPIKeyRepo spins up a fresh PostgreSQL 16 container, applies all
// migrations up to and including 20260622000003 (the polymorphic api_keys
// migration), and returns a pool, a UserRepository, a ServiceAccountRepo, and
// an APIKeyRepository that all share the same pool.
//
// The container and pool are cleaned up automatically by t.Cleanup.
func setupAPIKeyRepo(t *testing.T, ctx context.Context) (*pgxpool.Pool, *UserRepository, *ServiceAccountRepo, *APIKeyRepository) {
	t.Helper()

	// Spin up a fresh PostgreSQL 16 container.
	dsn := containers.Postgres(t)

	// Apply the FULL current migration set — this helper seeds via the live
	// UserRepository.Create, which targets the HEAD users schema (is_global_admin,
	// onboarding_complete, …). Pinning to an intermediate migration omits those
	// columns and the INSERT fails (FUT-085). gooseUp is in migrations_test.go.
	gooseUp(t, dsn)

	// Build the shared connection pool.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool, NewUserRepository(pool), NewServiceAccountRepo(pool), NewAPIKeyRepository(pool)
}

// seedHumanForKeys inserts a human user and returns (tenantID, userID).  The
// tenant UUID is generated freshly so each call produces an isolated namespace.
func seedHumanForKeys(t *testing.T, ctx context.Context, users *UserRepository, email string) (uuid.UUID, uuid.UUID) {
	t.Helper()

	tenant := uuid.New()
	username := "human-key-" + uuid.New().String()[:8]

	u, err := users.Create(ctx, CreateUserRequest{
		TenantID:     tenant,
		Username:     username,
		Email:        email,
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err, "seedHumanForKeys: Create")
	return tenant, u.ID
}

// seedSAForKeys creates a service account in the given tenant and returns the
// SA and its shadow user ID.  It uses CreateAtomic so the shadow user and SA
// row are always consistent.
func seedSAForKeys(t *testing.T, ctx context.Context, sa *ServiceAccountRepo, tenantID, creatorID uuid.UUID, name string) (*ServiceAccount, uuid.UUID) {
	t.Helper()

	created, shadowID, err := sa.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenantID,
		Name:      name,
		CreatedBy: creatorID,
	})
	require.NoError(t, err, "seedSAForKeys: CreateAtomic")
	return created, shadowID
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestAPIKeyRepo_PolymorphicCreate verifies that:
//   - a human-owned key (UserID set, ServiceAccountID nil) is created correctly;
//   - an SA-owned key (ServiceAccountID set, UserID nil) is created correctly;
//   - both owners set is rejected at the application layer;
//   - neither owner set is rejected at the application layer.
func TestAPIKeyRepo_PolymorphicCreate(t *testing.T) {
	ctx := context.Background()
	_, users, saRepo, keyRepo := setupAPIKeyRepo(t, ctx)

	tenant, humanID := seedHumanForKeys(t, ctx, users, "create@example.com")
	sa, _ := seedSAForKeys(t, ctx, saRepo, tenant, humanID, "create-test-sa")

	t.Run("human-owned key", func(t *testing.T) {
		// Create a key owned by the human user.
		key, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
			TenantID:  tenant,
			UserID:    &humanID,
			Name:      "human-key",
			KeyHash:   "hash-human",
			KeyPrefix: "pref-human",
			Scopes:    []string{"pull"},
		})
		require.NoError(t, err)
		require.NotNil(t, key)
		require.NotEqual(t, uuid.Nil, key.ID)
		// UserID must be non-nil and equal to humanID.
		require.NotNil(t, key.UserID, "human key must have UserID set")
		require.Equal(t, humanID, *key.UserID)
		// ServiceAccountID must be nil for a human-owned key.
		require.Nil(t, key.ServiceAccountID, "human key must have ServiceAccountID nil")
	})

	t.Run("SA-owned key", func(t *testing.T) {
		// Create a key owned by the service account.
		key, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
			TenantID:         tenant,
			ServiceAccountID: &sa.ID,
			Name:             "sa-key",
			KeyHash:          "hash-sa",
			KeyPrefix:        "pref-sa",
			Scopes:           []string{"pull", "push"},
		})
		require.NoError(t, err)
		require.NotNil(t, key)
		require.NotEqual(t, uuid.Nil, key.ID)
		// ServiceAccountID must be non-nil and equal to sa.ID.
		require.NotNil(t, key.ServiceAccountID, "SA key must have ServiceAccountID set")
		require.Equal(t, sa.ID, *key.ServiceAccountID)
		// UserID must be nil for an SA-owned key.
		require.Nil(t, key.UserID, "SA key must have UserID nil")
	})

	t.Run("both set is rejected", func(t *testing.T) {
		// Passing both UserID and ServiceAccountID must be rejected at the
		// application layer (before the database is consulted).
		_, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
			TenantID:         tenant,
			UserID:           &humanID,
			ServiceAccountID: &sa.ID,
			Name:             "both-key",
			KeyHash:          "h",
			KeyPrefix:        "p",
		})
		require.Error(t, err, "setting both UserID and ServiceAccountID must return an error")
	})

	t.Run("neither set is rejected", func(t *testing.T) {
		// Passing neither UserID nor ServiceAccountID must be rejected at the
		// application layer (before the database CHECK constraint fires).
		_, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
			TenantID:  tenant,
			Name:      "neither-key",
			KeyHash:   "h",
			KeyPrefix: "p",
		})
		require.Error(t, err, "setting neither UserID nor ServiceAccountID must return an error")
	})
}

// TestAPIKeyRepo_LookupReturnsOwner verifies that:
//   - GetByID for a human-owned key returns UserID non-nil and ServiceAccountID nil;
//   - GetByID for an SA-owned key returns ServiceAccountID non-nil and UserID nil;
//   - ListByUser returns the human key and excludes the SA-owned key;
//   - ListByServiceAccount returns the SA key and excludes the human-owned key.
func TestAPIKeyRepo_LookupReturnsOwner(t *testing.T) {
	ctx := context.Background()
	_, users, saRepo, keyRepo := setupAPIKeyRepo(t, ctx)

	tenant, humanID := seedHumanForKeys(t, ctx, users, "lookup@example.com")
	sa, _ := seedSAForKeys(t, ctx, saRepo, tenant, humanID, "lookup-test-sa")

	// Insert one human-owned key and one SA-owned key.
	humanKey, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
		TenantID:  tenant,
		UserID:    &humanID,
		Name:      "human-lookup",
		KeyHash:   "hh",
		KeyPrefix: "hp",
		Scopes:    []string{"pull"},
	})
	require.NoError(t, err)

	saKey, err := keyRepo.Create(ctx, CreateAPIKeyRequest{
		TenantID:         tenant,
		ServiceAccountID: &sa.ID,
		Name:             "sa-lookup",
		KeyHash:          "sh",
		KeyPrefix:        "sp",
		Scopes:           []string{"push"},
	})
	require.NoError(t, err)

	t.Run("GetByID human key", func(t *testing.T) {
		// GetByID must return the correct owner fields for the human key.
		got, err := keyRepo.GetByID(ctx, humanKey.ID)
		require.NoError(t, err)
		require.NotNil(t, got.UserID, "GetByID human key: UserID must be non-nil")
		require.Equal(t, humanID, *got.UserID)
		require.Nil(t, got.ServiceAccountID, "GetByID human key: ServiceAccountID must be nil")
	})

	t.Run("GetByID SA key returns ServiceAccountID", func(t *testing.T) {
		// GetByID must return the correct owner fields for the SA key.
		got, err := keyRepo.GetByID(ctx, saKey.ID)
		require.NoError(t, err)
		require.NotNil(t, got.ServiceAccountID, "GetByID SA key: ServiceAccountID must be non-nil")
		require.Equal(t, sa.ID, *got.ServiceAccountID)
		require.Nil(t, got.UserID, "GetByID SA key: UserID must be nil")
	})

	t.Run("ListByUser returns human key and excludes SA key", func(t *testing.T) {
		keys, err := keyRepo.ListByUser(ctx, humanID)
		require.NoError(t, err)

		// Extract IDs from the result for easy assertion.
		ids := make([]uuid.UUID, len(keys))
		for i, k := range keys {
			ids[i] = k.ID
		}
		require.Contains(t, ids, humanKey.ID, "ListByUser must include the human-owned key")
		require.NotContains(t, ids, saKey.ID, "ListByUser must not include SA-owned keys")
	})

	t.Run("ListByServiceAccount returns SA key and excludes human key", func(t *testing.T) {
		keys, err := keyRepo.ListByServiceAccount(ctx, sa.ID)
		require.NoError(t, err)

		// Extract IDs from the result for easy assertion.
		ids := make([]uuid.UUID, len(keys))
		for i, k := range keys {
			ids[i] = k.ID
		}
		require.Contains(t, ids, saKey.ID, "ListByServiceAccount must include the SA-owned key")
		require.NotContains(t, ids, humanKey.ID, "ListByServiceAccount must not include human-owned keys")
	})
}
