//go:build integration

// Package repository migrations_test exercises the goose migration round-trip
// for the users.kind column (Task 1, FE-API-048).  It runs against a real
// PostgreSQL 16 container via testcontainers and verifies that:
//   - goose Up to 20260622000001 adds the kind column with the correct
//     default and CHECK constraint;
//   - goose Down to 20260609000002 removes the column cleanly.
package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

// gooseUpTo runs all goose migrations up to and including the migration whose
// timestamp prefix matches versionPrefix.  versionPrefix must be the numeric
// portion of the filename, e.g. "20260622000001".
//
// It opens a separate *sql.DB over the same connection config as pool so that
// goose (which needs database/sql) can run alongside pgx.
func gooseUpTo(t *testing.T, dsn string, versionPrefix string) {
	t.Helper()

	// Parse the integer version from the prefix string.
	var version int64
	if _, err := fmt.Sscanf(versionPrefix, "%d", &version); err != nil {
		t.Fatalf("gooseUpTo: parse version %q: %v", versionPrefix, err)
	}

	sqlDB := openSQLDB(t, dsn)

	// Configure goose to read from the embedded FS each time; goose keeps
	// global state so we reset it here to avoid cross-test interference.
	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.UpTo(sqlDB, ".", version); err != nil {
		t.Fatalf("goose.UpTo(%d): %v", version, err)
	}
}

// gooseDownTo rolls back migrations until the database is at versionPrefix.
// After this call the schema only contains objects created by migrations
// numbered <= versionPrefix.
func gooseDownTo(t *testing.T, dsn string, versionPrefix string) {
	t.Helper()

	var version int64
	if _, err := fmt.Sscanf(versionPrefix, "%d", &version); err != nil {
		t.Fatalf("gooseDownTo: parse version %q: %v", versionPrefix, err)
	}

	sqlDB := openSQLDB(t, dsn)

	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.DownTo(sqlDB, ".", version); err != nil {
		t.Fatalf("goose.DownTo(%d): %v", version, err)
	}
}

// openSQLDB converts a DSN into a *sql.DB using the pgx stdlib adapter and
// registers a cleanup so it is closed at the end of the test.
func openSQLDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// TestMigration_UserKind_RoundTrip verifies the 20260622000001_user_kind
// migration applies and reverts correctly.
func TestMigration_UserKind_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// Start a fresh PostgreSQL container; the DSN is returned and the container
	// is cleaned up automatically via t.Cleanup registered by containers.Postgres.
	dsn := containers.Postgres(t)

	// Build a pgx pool for the query assertions.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// ── Step 1: migrate up to (and including) the target migration ──────────
	gooseUpTo(t, dsn, "20260622000001")

	// The seed migration (20260610000001) inserts two rows into users.  After
	// the kind migration those rows must have defaulted to 'human'.
	var kind string
	err = pool.QueryRow(ctx, `SELECT kind FROM users LIMIT 1`).Scan(&kind)
	require.NoError(t, err, "SELECT kind FROM users should succeed after migration")
	require.Equal(t, "human", kind, "existing rows must default to 'human'")

	// The CHECK constraint must reject values outside ('human', 'service_account').
	_, err = pool.Exec(ctx,
		`UPDATE users SET kind = 'robot' WHERE id = (SELECT id FROM users LIMIT 1)`,
	)
	require.Error(t, err, "CHECK constraint should reject unknown kind value")

	// ── Step 2: migrate back down to the api_keys migration ─────────────────
	gooseDownTo(t, dsn, "20260609000002")

	// After rolling back, the kind column must not exist.
	var hasCol bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM information_schema.columns
			WHERE table_name = 'users'
			  AND column_name = 'kind'
		)`,
	).Scan(&hasCol)
	require.NoError(t, err, "information_schema query should succeed")
	require.False(t, hasCol, "kind column must be absent after Down migration")
}

// TestMigration_ServiceAccounts_UniqueAndCascade verifies the
// 20260622000002_service_accounts migration:
//   - UNIQUE (tenant_id, name) rejects a duplicate name within the same tenant;
//   - ON DELETE SET NULL on created_by nulls the column when the creator is deleted;
//   - ON DELETE CASCADE on shadow_user_id removes the service_accounts row when the
//     shadow user is deleted.
func TestMigration_ServiceAccounts_UniqueAndCascade(t *testing.T) {
	ctx := context.Background()

	// Start a fresh PostgreSQL container; containers.Postgres registers cleanup
	// via t.Cleanup so no explicit teardown is needed here.
	dsn := containers.Postgres(t)

	// Build a pgx pool for the assertion queries below.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// Migrate up through the service_accounts migration.
	gooseUpTo(t, dsn, "20260622000002")

	tenant := uuid.New()

	// Seed a "creator" human user whose deletion later nulls created_by.
	var creatorID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'admin', 'admin@example.com', '', 'human')
		RETURNING id`, tenant).Scan(&creatorID))

	// Shadow user 1 — will be the backing identity for the first SA.
	var shadow1 uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'sa-ci-prod', 'sa+1@internal.invalid', '', 'service_account')
		RETURNING id`, tenant).Scan(&shadow1))

	// Insert the first service account; capture its id for later assertions.
	var sa1 uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO service_accounts (tenant_id, shadow_user_id, name, created_by)
		VALUES ($1, $2, 'ci-prod', $3) RETURNING id`,
		tenant, shadow1, creatorID).Scan(&sa1))

	// Shadow user 2 — will be used in the duplicate-name attempt.
	var shadow2 uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'sa-ci-prod-2', 'sa+2@internal.invalid', '', 'service_account')
		RETURNING id`, tenant).Scan(&shadow2))

	// A second SA with the same name in the same tenant must violate the UNIQUE
	// constraint on (tenant_id, name).
	_, err = pool.Exec(ctx, `
		INSERT INTO service_accounts (tenant_id, shadow_user_id, name, created_by)
		VALUES ($1, $2, 'ci-prod', $3)`, tenant, shadow2, creatorID)
	require.Error(t, err, "UNIQUE (tenant_id, name) should reject duplicate name within tenant")

	// Deleting the creator must set created_by to NULL (ON DELETE SET NULL).
	_, err = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, creatorID)
	require.NoError(t, err)
	var createdBy *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT created_by FROM service_accounts WHERE id=$1`, sa1).Scan(&createdBy))
	require.Nil(t, createdBy, "created_by must be NULL after creator user is deleted")

	// Deleting the shadow user must cascade-delete the service_accounts row
	// (ON DELETE CASCADE on shadow_user_id).
	_, err = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, shadow1)
	require.NoError(t, err)
	var saCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM service_accounts WHERE id=$1`, sa1).Scan(&saCount))
	require.Equal(t, 0, saCount, "service_accounts row must cascade-delete when shadow user is deleted")
}
