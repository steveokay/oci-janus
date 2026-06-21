//go:build integration

// Package repository — TestServiceAccountRepo covers the ServiceAccountRepo
// methods mandated by FE-API-048 Task 5:
//
//   - CreateAtomic inserts both the shadow user and the SA row in one
//     transaction; the shadow user's email is sa+<sa_id>@internal.invalid and
//     its kind is 'service_account'.
//   - Delete cascades to the shadow user and (via FK) to any api_keys rows.
//   - Update handles optional field patching and Disabled toggling.
//   - CountKeysAffectedByScopeShrink correctly counts active keys with
//     out-of-bounds scopes.
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

// ── helpers ──────────────────────────────────────────────────────────────────

// setupSARepo boots a fresh PostgreSQL container, applies all migrations up to
// and including 20260622000003 (the polymorphic api_keys migration that adds
// service_account_id), and returns the pool, a UserRepository, and a
// ServiceAccountRepo that share that pool.
//
// The container and pool are cleaned up automatically by t.Cleanup.
func setupSARepo(t *testing.T, ctx context.Context) (*pgxpool.Pool, *UserRepository, *ServiceAccountRepo) {
	t.Helper()

	// Spin up a fresh PostgreSQL 16 container. containers.Postgres registers
	// its own t.Cleanup for the container lifetime.
	dsn := containers.Postgres(t)

	// Apply all schema migrations that this test depends on.
	// gooseUpTo is defined in migrations_test.go (same package + build tag).
	gooseUpTo(t, dsn, "20260622000003")

	// Build the pgxpool used by both repositories.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool, NewUserRepository(pool), NewServiceAccountRepo(pool)
}

// seedHuman inserts a human user into the given tenant and returns (tenantID,
// userID). The tenant UUID is generated freshly so each call produces an
// isolated namespace; callers that need multiple users in the same tenant should
// pass the same tenantID to repo.Create directly after the first call.
func seedHuman(t *testing.T, ctx context.Context, users *UserRepository, email string) (uuid.UUID, uuid.UUID) {
	t.Helper()

	tenant := uuid.New()
	// Derive a unique username from the email's local-part to satisfy the
	// UNIQUE (tenant_id, username) constraint even when seedHuman is called
	// multiple times with the same email in different tenants.
	username := "human-" + uuid.New().String()[:8]

	u, err := users.Create(ctx, CreateUserRequest{
		TenantID:     tenant,
		Username:     username,
		Email:        email,
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err, "seedHuman: Create")
	return tenant, u.ID
}

// seedHumanInTenant inserts a human user into an existing tenant. Used when a
// test needs both a creator and additional actors in the same tenant namespace.
func seedHumanInTenant(t *testing.T, ctx context.Context, users *UserRepository, tenantID uuid.UUID, email string) uuid.UUID {
	t.Helper()

	username := "human-" + uuid.New().String()[:8]
	u, err := users.Create(ctx, CreateUserRequest{
		TenantID:     tenantID,
		Username:     username,
		Email:        email,
		PasswordHash: "x",
		Kind:         "human",
	})
	require.NoError(t, err, "seedHumanInTenant: Create")
	return u.ID
}

// getUserKind reads the kind column for the given user id directly from the
// pool so tests can assert on the shadow user without going through
// UserRepository's kind guards.
func getUserKind(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	var kind string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT kind FROM users WHERE id = $1`, userID).Scan(&kind),
		"getUserKind: SELECT kind")
	return kind
}

// getUserEmail reads the email column for the given user id directly.
func getUserEmail(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) string {
	t.Helper()
	var email string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(email, '') FROM users WHERE id = $1`, userID).Scan(&email),
		"getUserEmail: SELECT email")
	return email
}

// countRows returns the number of rows in tableName matching the given WHERE
// clause fragment and args. The fragment must use $1, $2… placeholders
// (positional). Example: countRows(t, ctx, pool, "users", "id=$1", someID).
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName, where string, args ...any) int {
	t.Helper()
	var n int
	q := "SELECT count(*) FROM " + tableName + " WHERE " + where
	require.NoError(t, pool.QueryRow(ctx, q, args...).Scan(&n),
		"countRows: SELECT count(*) FROM "+tableName)
	return n
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestServiceAccountRepo_CreateAtomic verifies that CreateAtomic:
//   - returns a non-nil SA with the supplied name
//   - generates a non-nil SA id
//   - creates a shadow user with kind='service_account'
//   - sets the shadow user's email to sa+<sa_id>@internal.invalid
func TestServiceAccountRepo_CreateAtomic(t *testing.T) {
	ctx := context.Background()
	pool, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin@example.com")

	sa, shadowID, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          "ci-prod",
		AllowedScopes: []string{"pull", "push"},
		CreatedBy:     creator,
	})
	require.NoError(t, err)
	require.NotNil(t, sa)
	require.NotEqual(t, uuid.Nil, sa.ID, "SA id must be non-nil")
	require.Equal(t, "ci-prod", sa.Name)
	require.Equal(t, tenant, sa.TenantID)
	require.Equal(t, []string{"pull", "push"}, sa.AllowedScopes)
	require.NotNil(t, sa.CreatedBy)
	require.Equal(t, creator, *sa.CreatedBy)

	// Shadow user assertions — verified via the raw pool so kind guards don't
	// interfere.
	require.Equal(t, "service_account", getUserKind(t, ctx, pool, shadowID),
		"shadow user must have kind='service_account'")
	require.Equal(t, "sa+"+sa.ID.String()+"@internal.invalid", getUserEmail(t, ctx, pool, shadowID),
		"shadow user email must be sa+<sa_id>@internal.invalid")

	// The SA row must reference the shadow user.
	require.Equal(t, shadowID, sa.ShadowUserID)
}

// TestServiceAccountRepo_CreateAtomic_NilScopes verifies that passing a nil
// AllowedScopes stores an empty array (not NULL) so the Postgres NOT NULL
// constraint is satisfied and callers can always iterate the slice safely.
func TestServiceAccountRepo_CreateAtomic_NilScopes(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin2@example.com")

	sa, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          "nil-scopes",
		AllowedScopes: nil, // intentionally nil
		CreatedBy:     creator,
	})
	require.NoError(t, err)
	// AllowedScopes must be a non-nil empty slice, not nil.
	require.NotNil(t, sa.AllowedScopes, "AllowedScopes must not be nil after nil input")
	require.Empty(t, sa.AllowedScopes, "AllowedScopes must be empty slice for nil input")
}

// TestServiceAccountRepo_CreateAtomic_DuplicateName verifies that inserting a
// second SA with the same name in the same tenant returns ErrAlreadyExists
// (UNIQUE (tenant_id, name) violation).
func TestServiceAccountRepo_CreateAtomic_DuplicateName(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "dup@example.com")

	_, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "same-name",
		CreatedBy: creator,
	})
	require.NoError(t, err, "first SA should be created without error")

	_, _, err = repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "same-name",
		CreatedBy: creator,
	})
	require.ErrorIs(t, err, ErrAlreadyExists,
		"duplicate name in the same tenant must return ErrAlreadyExists")
}

// TestServiceAccountRepo_DeleteCascades verifies that Delete:
//   - hard-deletes the shadow user
//   - cascades to the service_accounts row (via ON DELETE CASCADE)
//   - returns ErrNotFound when called a second time on the same id
func TestServiceAccountRepo_DeleteCascades(t *testing.T) {
	ctx := context.Background()
	pool, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin3@example.com")

	sa, shadowID, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "to-delete",
		CreatedBy: creator,
	})
	require.NoError(t, err)

	// Confirm both rows exist before deletion.
	require.Equal(t, 1, countRows(t, ctx, pool, "service_accounts", "id=$1", sa.ID))
	require.Equal(t, 1, countRows(t, ctx, pool, "users", "id=$1", shadowID))

	// Delete should succeed.
	require.NoError(t, repo.Delete(ctx, sa.ID))

	// Both rows must have been removed.
	require.Equal(t, 0, countRows(t, ctx, pool, "service_accounts", "id=$1", sa.ID),
		"service_accounts row must be gone after Delete")
	require.Equal(t, 0, countRows(t, ctx, pool, "users", "id=$1", shadowID),
		"shadow user must be gone after Delete (ON DELETE CASCADE from service_accounts)")

	// A second Delete on the same id must return ErrNotFound.
	err = repo.Delete(ctx, sa.ID)
	require.ErrorIs(t, err, ErrNotFound,
		"Delete on already-deleted SA must return ErrNotFound")
}

// TestServiceAccountRepo_Get verifies that Get returns the correct SA by id
// and returns ErrNotFound for an unknown id.
func TestServiceAccountRepo_Get(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin4@example.com")

	created, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          "get-me",
		Description:   "test description",
		AllowedScopes: []string{"pull"},
		CreatedBy:     creator,
	})
	require.NoError(t, err)

	got, err := repo.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, "get-me", got.Name)
	require.Equal(t, "test description", got.Description)
	require.Equal(t, []string{"pull"}, got.AllowedScopes)
	require.Nil(t, got.DisabledAt, "newly created SA must not be disabled")

	// Unknown id must return ErrNotFound.
	_, err = repo.Get(ctx, uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

// TestServiceAccountRepo_Update_Disable verifies that Update with Disabled=true
// sets disabled_at to a non-nil timestamp, and Disabled=false clears it.
func TestServiceAccountRepo_Update_Disable(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin5@example.com")

	sa, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "toggle-disabled",
		CreatedBy: creator,
	})
	require.NoError(t, err)
	require.Nil(t, sa.DisabledAt, "SA must start enabled")

	// Disable the SA.
	disabled := true
	updated, err := repo.Update(ctx, UpdateServiceAccountInput{
		ID:       sa.ID,
		TenantID: tenant,
		Disabled: &disabled,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.DisabledAt, "disabled_at must be set after Disable=true")

	// Re-enable the SA.
	notDisabled := false
	reEnabled, err := repo.Update(ctx, UpdateServiceAccountInput{
		ID:       sa.ID,
		TenantID: tenant,
		Disabled: &notDisabled,
	})
	require.NoError(t, err)
	require.Nil(t, reEnabled.DisabledAt, "disabled_at must be cleared after Disabled=false")
}

// TestServiceAccountRepo_Update_Name verifies that Update patches the name
// when Name is non-nil, and leaves other columns untouched.
func TestServiceAccountRepo_Update_Name(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin6@example.com")

	sa, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          "old-name",
		Description:   "keep this",
		AllowedScopes: []string{"pull"},
		CreatedBy:     creator,
	})
	require.NoError(t, err)

	newName := "new-name"
	updated, err := repo.Update(ctx, UpdateServiceAccountInput{
		ID:       sa.ID,
		TenantID: tenant,
		Name:     &newName,
		// Description and AllowedScopes nil → unchanged.
	})
	require.NoError(t, err)
	require.Equal(t, "new-name", updated.Name)
	require.Equal(t, "keep this", updated.Description, "description must be unchanged")
	require.Equal(t, []string{"pull"}, updated.AllowedScopes, "scopes must be unchanged")
}

// TestServiceAccountRepo_Update_WrongTenant verifies that Update returns
// ErrNotFound when the tenantID does not match the SA's tenant, preventing
// cross-tenant updates.
func TestServiceAccountRepo_Update_WrongTenant(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin7@example.com")

	sa, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "cross-tenant-target",
		CreatedBy: creator,
	})
	require.NoError(t, err)

	wrongTenant := uuid.New()
	newName := "hacked"
	_, err = repo.Update(ctx, UpdateServiceAccountInput{
		ID:       sa.ID,
		TenantID: wrongTenant,
		Name:     &newName,
	})
	require.ErrorIs(t, err, ErrNotFound,
		"Update with wrong tenant must return ErrNotFound")
}

// TestServiceAccountRepo_List_Basic verifies that List returns created SAs for
// the correct tenant and respects the includeDisabled flag.
func TestServiceAccountRepo_List_Basic(t *testing.T) {
	ctx := context.Background()
	_, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin8@example.com")

	// Create two SAs: one active, one to be disabled.
	saActive, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "active-sa",
		CreatedBy: creator,
	})
	require.NoError(t, err)

	saToDisable, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "disabled-sa",
		CreatedBy: creator,
	})
	require.NoError(t, err)

	// Disable the second SA.
	disabled := true
	_, err = repo.Update(ctx, UpdateServiceAccountInput{
		ID:       saToDisable.ID,
		TenantID: tenant,
		Disabled: &disabled,
	})
	require.NoError(t, err)

	// List without disabled — should only return the active SA.
	active, _, err := repo.List(ctx, tenant, false, 20, "")
	require.NoError(t, err)
	require.Len(t, active, 1, "only the active SA should be returned when includeDisabled=false")
	require.Equal(t, saActive.ID, active[0].ID)

	// List with disabled — should return both SAs.
	all, _, err := repo.List(ctx, tenant, true, 20, "")
	require.NoError(t, err)
	require.Len(t, all, 2, "both SAs should be returned when includeDisabled=true")

	// A different tenant must see zero rows.
	otherTenant := uuid.New()
	none, _, err := repo.List(ctx, otherTenant, true, 20, "")
	require.NoError(t, err)
	require.Empty(t, none, "different tenant must see no SAs")
}

// TestServiceAccountRepo_CountKeysAffectedByScopeShrink verifies that:
//   - a key whose scopes are a subset of proposed returns count=0 (safe)
//   - a key that grants a scope absent from proposed returns count=1 (warned)
func TestServiceAccountRepo_CountKeysAffectedByScopeShrink(t *testing.T) {
	ctx := context.Background()
	pool, users, repo := setupSARepo(t, ctx)

	tenant, creator := seedHuman(t, ctx, users, "admin9@example.com")

	sa, shadowID, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          "scope-test",
		AllowedScopes: []string{"pull", "push", "delete"},
		CreatedBy:     creator,
	})
	require.NoError(t, err)

	// Insert an active key with scopes ["pull", "push"] owned by the SA.
	// api_keys.service_account_id was added by migration 20260622000003.
	_, err = pool.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, service_account_id, name, key_hash, key_prefix, scopes, is_active)
		VALUES ($1, $2, 'k1', 'h', 'p', $3, true)`,
		tenant, sa.ID, []string{"pull", "push"})
	require.NoError(t, err)

	// We also insert a key for the shadow user directly (not SA-owned) to
	// confirm it is excluded from the count.
	_, err = pool.Exec(ctx, `
		INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes, is_active)
		VALUES ($1, $2, 'shadow-k', 'h2', 'p2', $3, true)`,
		tenant, shadowID, []string{"delete"})
	require.NoError(t, err)

	// Proposed scopes that include everything the key has → count = 0 (safe).
	n, err := repo.CountKeysAffectedByScopeShrink(ctx, sa.ID, []string{"pull", "push", "delete"})
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "no keys should be affected when proposed ⊇ key scopes")

	// Proposed scopes that drop 'push' → the key has 'push' → count = 1.
	n, err = repo.CountKeysAffectedByScopeShrink(ctx, sa.ID, []string{"pull"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "one key should be affected when proposed ⊉ key scopes")

	// Empty proposed scopes → all keys with any scope are affected.
	n, err = repo.CountKeysAffectedByScopeShrink(ctx, sa.ID, []string{})
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "all keys are affected when proposed scopes is empty")
}
