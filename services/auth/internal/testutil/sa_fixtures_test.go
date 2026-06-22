//go:build integration

// Package testutil_test exercises the SA fixture helpers in sa_fixtures.go
// against a real PostgreSQL container. These are smoke tests — they verify
// that each helper completes without error and that the resulting rows are
// visible in the database. Full lifecycle assertions (e.g. API-key
// authentication through the service layer) live in the activity-facade
// integration test (T19).
package testutil_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	authtestutil "github.com/steveokay/oci-janus/services/auth/internal/testutil"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// newAuthPool spins up an auth PostgreSQL container, runs all auth migrations,
// and returns a ready connection pool. The container is torn down when t
// finishes. This helper is local to the test file because the sa_fixtures
// package itself cannot import the migration FS (that would create a cycle).
func newAuthPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("newAuthPool: pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("newAuthPool: ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("newAuthPool: goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("newAuthPool: goose.Up: %v", err)
	}

	return pool
}

// TestNewServiceAccount_Inserts verifies that NewServiceAccount creates a
// service_accounts row that is retrievable via ServiceAccountRepo.Get.
func TestNewServiceAccount_Inserts(t *testing.T) {
	ctx := context.Background()
	pool := newAuthPool(t)

	repo := repository.NewServiceAccountRepo(pool)
	users := repository.NewUserRepository(pool)

	tenant := uuid.New()

	sa, shadowUserID := authtestutil.NewServiceAccount(t, ctx, repo, users, tenant, "test-sa", "push", "pull")

	// The returned SA must have a non-nil ID and match the input name/tenant.
	if sa.ID == uuid.Nil {
		t.Fatal("NewServiceAccount: expected non-nil SA ID")
	}
	if sa.TenantID != tenant {
		t.Fatalf("NewServiceAccount: tenant mismatch: got %s, want %s", sa.TenantID, tenant)
	}
	if sa.Name != "test-sa" {
		t.Fatalf("NewServiceAccount: name mismatch: got %q, want %q", sa.Name, "test-sa")
	}
	if shadowUserID == uuid.Nil {
		t.Fatal("NewServiceAccount: expected non-nil shadow user ID")
	}
	if sa.ShadowUserID != shadowUserID {
		t.Fatalf("NewServiceAccount: ShadowUserID mismatch: got %s, want %s", sa.ShadowUserID, shadowUserID)
	}

	// Confirm the row is visible via Get.
	got, err := repo.Get(ctx, sa.ID)
	if err != nil {
		t.Fatalf("NewServiceAccount: repo.Get after insert: %v", err)
	}
	if got.ID != sa.ID {
		t.Fatalf("NewServiceAccount: Get returned wrong ID: got %s, want %s", got.ID, sa.ID)
	}
}

// TestNewAPIKeyForSA_Issues verifies that NewAPIKeyForSA creates an api_keys
// row whose ServiceAccountID points to the given SA.
func TestNewAPIKeyForSA_Issues(t *testing.T) {
	ctx := context.Background()
	pool := newAuthPool(t)

	repo := repository.NewServiceAccountRepo(pool)
	users := repository.NewUserRepository(pool)
	keys := repository.NewAPIKeyRepository(pool)

	tenant := uuid.New()

	sa, _ := authtestutil.NewServiceAccount(t, ctx, repo, users, tenant, "sa-for-key", "pull")

	keyID, rawSecret := authtestutil.NewAPIKeyForSA(t, ctx, keys, sa, "ci-robot", "pull")

	if keyID == "" {
		t.Fatal("NewAPIKeyForSA: expected non-empty key ID")
	}
	if rawSecret == "" {
		t.Fatal("NewAPIKeyForSA: expected non-empty raw secret")
	}
	// Raw secret must be a 64-char lowercase hex string (32 random bytes).
	if len(rawSecret) != 64 {
		t.Fatalf("NewAPIKeyForSA: raw secret length %d, want 64", len(rawSecret))
	}

	// Retrieve the key and assert ownership.
	keyUUID, err := uuid.Parse(keyID)
	if err != nil {
		t.Fatalf("NewAPIKeyForSA: parse key ID %q: %v", keyID, err)
	}
	key, err := keys.GetByID(ctx, keyUUID)
	if err != nil {
		t.Fatalf("NewAPIKeyForSA: GetByID: %v", err)
	}
	if key.ServiceAccountID == nil {
		t.Fatal("NewAPIKeyForSA: key.ServiceAccountID is nil")
	}
	if *key.ServiceAccountID != sa.ID {
		t.Fatalf("NewAPIKeyForSA: ServiceAccountID mismatch: got %s, want %s", *key.ServiceAccountID, sa.ID)
	}
	if key.UserID != nil {
		t.Fatal("NewAPIKeyForSA: expected UserID to be nil for an SA-owned key")
	}
}
