//go:build integration

// Package repository integration tests for deployment_metadata Get/Set methods.
// Exercises the real SQL paths against a testcontainers PostgreSQL instance with
// the full migration set applied. Run with:
//
//	go test -tags integration ./services/tenant/internal/repository/... -v -run TestDeploymentMetadata
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	tenantmigrations "github.com/steveokay/oci-janus/services/tenant/migrations"
)

// setupDeploymentMetadataDB starts a fresh PostgreSQL container, runs all tenant
// migrations, and returns a pgxpool connected to it. The container and pool are
// cleaned up automatically via t.Cleanup.
func setupDeploymentMetadataDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// Start a fresh PostgreSQL 16 container via the shared testutil helper.
	// The container is terminated on t.Cleanup automatically.
	dsn := containers.Postgres(t)

	// Open a database/sql connection so goose (which needs database/sql) can
	// apply migrations. stdlib.OpenDB reuses the pgx driver without a separate
	// import.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Apply all tenant migrations including 20260627000001_deployment_metadata.
	goose.SetBaseFS(tenantmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	// Ensure goose used correctly; verify deployment_metadata exists.
	var exists bool
	row := sqlDB.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'deployment_metadata'
		)`)
	if err := row.Scan(&exists); err != nil || !exists {
		t.Fatalf("deployment_metadata table not found after migration (exists=%v, err=%v)", exists, err)
	}

	// Return a pgxpool for the repository under test.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mustJSON marshals v to compact JSON or fails the test.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return json.RawMessage(b)
}

// TestDeploymentMetadata_NotSet verifies that GetDeploymentMetadata returns
// ErrNotFound for a key that has never been written.
// Per spec: callers treat this as "value is zero," not as a hard error.
func TestDeploymentMetadata_NotSet(t *testing.T) {
	pool := setupDeploymentMetadataDB(t)
	repo := New(pool)
	ctx := context.Background()

	// A key that was never inserted must surface as ErrNotFound, not as a DB
	// error or a nil value with nil error.
	got, err := repo.GetDeploymentMetadata(ctx, "bootstrap_tenant_id")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDeploymentMetadata(unset key): want ErrNotFound, got err=%v", err)
	}
	if got != nil {
		t.Errorf("GetDeploymentMetadata(unset key): want nil value, got %s", got)
	}
}

// TestDeploymentMetadata_SetThenGet verifies that a value written via
// SetDeploymentMetadata is faithfully returned by GetDeploymentMetadata.
func TestDeploymentMetadata_SetThenGet(t *testing.T) {
	pool := setupDeploymentMetadataDB(t)
	repo := New(pool)
	ctx := context.Background()

	key := "bootstrap_tenant_id"
	// Use a JSON-string UUID as the value, matching the intended production use.
	want := mustJSON(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890")

	if err := repo.SetDeploymentMetadata(ctx, key, want); err != nil {
		t.Fatalf("SetDeploymentMetadata: %v", err)
	}

	got, err := repo.GetDeploymentMetadata(ctx, key)
	if err != nil {
		t.Fatalf("GetDeploymentMetadata: %v", err)
	}

	// Compare as normalised JSON strings so whitespace differences don't matter.
	var wantNorm, gotNorm any
	if err := json.Unmarshal(want, &wantNorm); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(got, &gotNorm); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if wantNorm != gotNorm {
		t.Errorf("round-trip mismatch: got %s, want %s", got, want)
	}
}

// TestDeploymentMetadata_Upsert verifies that SetDeploymentMetadata called twice
// with the same key updates the value rather than returning a duplicate-key error.
func TestDeploymentMetadata_Upsert(t *testing.T) {
	pool := setupDeploymentMetadataDB(t)
	repo := New(pool)
	ctx := context.Background()

	key := "upsert_test_key"
	first := mustJSON(t, "first-value")
	second := mustJSON(t, "second-value")

	// First insert.
	if err := repo.SetDeploymentMetadata(ctx, key, first); err != nil {
		t.Fatalf("SetDeploymentMetadata (first): %v", err)
	}
	// Second call with the same key must not error — ON CONFLICT DO UPDATE.
	if err := repo.SetDeploymentMetadata(ctx, key, second); err != nil {
		t.Fatalf("SetDeploymentMetadata (second/upsert): %v", err)
	}
}

// TestDeploymentMetadata_GetAfterUpsert verifies that GetDeploymentMetadata
// returns the LATEST value when the same key has been set more than once.
func TestDeploymentMetadata_GetAfterUpsert(t *testing.T) {
	pool := setupDeploymentMetadataDB(t)
	repo := New(pool)
	ctx := context.Background()

	key := "get_after_upsert_key"
	first := mustJSON(t, "original")
	second := mustJSON(t, "updated")

	// Write twice.
	if err := repo.SetDeploymentMetadata(ctx, key, first); err != nil {
		t.Fatalf("SetDeploymentMetadata (first): %v", err)
	}
	if err := repo.SetDeploymentMetadata(ctx, key, second); err != nil {
		t.Fatalf("SetDeploymentMetadata (second): %v", err)
	}

	// Get must return the second (latest) value.
	got, err := repo.GetDeploymentMetadata(ctx, key)
	if err != nil {
		t.Fatalf("GetDeploymentMetadata: %v", err)
	}

	var wantNorm, gotNorm any
	if err := json.Unmarshal(second, &wantNorm); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(got, &gotNorm); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if wantNorm != gotNorm {
		t.Errorf("GetAfterUpsert: got %s, want %s (latest)", got, second)
	}
}

// Compile-time guard: ensure we didn't accidentally reference the wrong sql
// package. This blank import keeps the stdlib.OpenDB call from being flagged
// as unused by the linter when the only usage is inside setupDeploymentMetadataDB.
var _ *sql.DB
