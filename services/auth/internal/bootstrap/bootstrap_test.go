//go:build integration

// Package bootstrap_test contains integration tests for the bootstrap CLI.
//
// Each test spins up two fresh PostgreSQL 16 containers (auth + tenant) via
// testcontainers and calls RunWithConfig directly — no subprocess, no stdin
// piping, no environment variable ceremony.
//
// Tenant migrations are provided inline via testing/fstest.MapFS so this test
// does NOT import services/tenant/migrations (which would add services/tenant
// to services/auth's go.mod and violate the monorepo module boundary rules).
// The SQL content is copied verbatim from services/tenant/migrations/*.sql.
// If those files change, update the corresponding entries below.
package bootstrap_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/bootstrap"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ── Inline tenant migrations ──────────────────────────────────────────────────
//
// These SQL files are copied verbatim from services/tenant/migrations/*.sql.
// They live here as an in-memory fs.FS consumed by applyMigrations so this
// test file does not need to import services/tenant (which would modify
// services/auth/go.mod). If services/tenant/migrations/*.sql change, update
// the corresponding entries here.

// tenantMigrationsFS mirrors the six tenant migration files needed to produce
// a schema identical to what services/tenant runs in production.
var tenantMigrationsFS = fstest.MapFS{
	"20240101000001_create_tenant_tables.sql": &fstest.MapFile{Data: []byte(`-- +goose Up

CREATE TABLE tenants (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL UNIQUE,
    plan       TEXT        NOT NULL DEFAULT 'standard',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenant_policies (
    tenant_id              UUID    PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    scan_on_push           BOOLEAN NOT NULL DEFAULT true,
    block_on_severity      TEXT    NOT NULL DEFAULT 'CRITICAL',
    allow_unscanned        BOOLEAN NOT NULL DEFAULT false,
    proxy_cache_enabled    BOOLEAN NOT NULL DEFAULT true,
    signing_required       BOOLEAN NOT NULL DEFAULT false,
    exempt_repositories    TEXT[]  NOT NULL DEFAULT '{}',
    storage_quota_bytes    BIGINT  NOT NULL DEFAULT 107374182400
);

CREATE TABLE tenant_domains (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain              TEXT        NOT NULL UNIQUE,
    verification_token  TEXT        NOT NULL,
    verified            BOOLEAN     NOT NULL DEFAULT false,
    registered_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at         TIMESTAMPTZ
);

CREATE INDEX idx_tenant_domains_tenant ON tenant_domains(tenant_id);
CREATE INDEX idx_tenant_domains_unverified ON tenant_domains(verified, registered_at)
    WHERE verified = false;

-- +goose Down

DROP TABLE IF EXISTS tenant_domains;
DROP TABLE IF EXISTS tenant_policies;
DROP TABLE IF EXISTS tenants;
`)},

	"20260611000001_domain_notification.sql": &fstest.MapFile{Data: []byte(`-- +goose Up

ALTER TABLE tenant_domains
    ADD COLUMN notified_24h    BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN notified_48h    BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN next_poll_after TIMESTAMPTZ NOT NULL DEFAULT now();

DROP INDEX IF EXISTS idx_tenant_domains_unverified;
CREATE INDEX idx_tenant_domains_unverified
    ON tenant_domains(next_poll_after, registered_at)
    WHERE verified = false;

-- +goose Down

ALTER TABLE tenant_domains
    DROP COLUMN IF EXISTS notified_24h,
    DROP COLUMN IF EXISTS notified_48h,
    DROP COLUMN IF EXISTS next_poll_after;

DROP INDEX IF EXISTS idx_tenant_domains_unverified;
CREATE INDEX idx_tenant_domains_unverified ON tenant_domains(verified, registered_at)
    WHERE verified = false;
`)},

	"20260620000001_add_tenant_slug.sql": &fstest.MapFile{Data: []byte(`-- +goose Up

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS slug TEXT;

UPDATE tenants
SET slug = trim(BOTH '-' FROM regexp_replace(
        regexp_replace(lower(name), '[^a-z0-9]+', '-', 'g'),
        '-+', '-', 'g'))
WHERE slug IS NULL OR slug = '';

UPDATE tenants
SET slug = id::text
WHERE slug IS NULL OR slug = '';

ALTER TABLE tenants
    ALTER COLUMN slug SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_slug_unique ON tenants(slug);

-- +goose Down

DROP INDEX IF EXISTS idx_tenants_slug_unique;
ALTER TABLE tenants DROP COLUMN IF EXISTS slug;
`)},

	"20260620000002_add_domain_is_primary.sql": &fstest.MapFile{Data: []byte(`-- +goose Up

ALTER TABLE tenant_domains
    ADD COLUMN IF NOT EXISTS is_primary BOOLEAN NOT NULL DEFAULT FALSE;

WITH first_verified AS (
    SELECT DISTINCT ON (tenant_id) id
    FROM tenant_domains
    WHERE verified = TRUE
    ORDER BY tenant_id,
             verified_at NULLS LAST,
             registered_at,
             id
)
UPDATE tenant_domains td
SET is_primary = TRUE
FROM first_verified fv
WHERE td.id = fv.id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_domains_one_primary
    ON tenant_domains(tenant_id)
    WHERE is_primary;

-- +goose Down

DROP INDEX IF EXISTS idx_tenant_domains_one_primary;
ALTER TABLE tenant_domains DROP COLUMN IF EXISTS is_primary;
`)},

	"20260623120000_seed_dev_tenant.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
-- +goose StatementBegin

INSERT INTO tenants (id, name, plan, slug, created_at)
VALUES (
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e',
    'Dev',
    'free',
    'dev',
    now()
)
ON CONFLICT (id) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM tenants WHERE id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e';

-- +goose StatementEnd
`)},

	"20260627000001_deployment_metadata.sql": &fstest.MapFile{Data: []byte(`-- +goose Up

CREATE TABLE deployment_metadata (
    key        TEXT        PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS deployment_metadata;
`)},
}

// ── Container helpers ─────────────────────────────────────────────────────────

// startPostgres starts a fresh PostgreSQL 16-alpine container and returns its
// DSN. The container is stopped automatically when t finishes.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "test",
				"POSTGRES_PASSWORD": "test",
				"POSTGRES_DB":       "testdb",
			},
			// Wait for two occurrences: once when initdb starts, once when the
			// server is ready to accept connections after startup.
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		},
		Started: true,
	})
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "5432")
	require.NoError(t, err)

	return fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", host, port.Port())
}

// runMigrations applies all goose Up migrations from migrFS against the
// database at dsn. Both embed.FS and fstest.MapFS satisfy fs.FS.
func runMigrations(t *testing.T, dsn string, migrFS fs.FS) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse DSN for migrations")
	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(migrFS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(sqlDB, "."), "apply migrations to %s", dsn)
}

// setupDatabases starts two independent Postgres containers (auth + tenant),
// applies the respective migrations, and returns the two DSNs.
func setupDatabases(t *testing.T) (authDSN, tenantDSN string) {
	t.Helper()

	authDSN = startPostgres(t)
	tenantDSN = startPostgres(t)

	// Auth migrations come from the embedded FS in this module.
	runMigrations(t, authDSN, authmigrations.FS)

	// Tenant migrations are inlined as fstest.MapFS so we don't need to import
	// services/tenant (which would add it to services/auth's go.mod).
	runMigrations(t, tenantDSN, tenantMigrationsFS)

	return authDSN, tenantDSN
}

// baseConfig returns a valid Config populated with the given DSNs.
// Tests override individual fields as needed.
func baseConfig(authDSN, tenantDSN string) bootstrap.Config {
	return bootstrap.Config{
		AdminEmail:     "admin@example.com",
		AdminUsername:  "bootstrapadmin",
		TenantName:     "acme",
		TenantID:       uuid.Nil, // generated by Run
		AuthDBDSN:      authDSN,
		TenantDBDSN:    tenantDSN,
		DeploymentMode: "single",
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestBootstrap_FreshDB is the happy-path test. It runs bootstrap against two
// empty databases and verifies that the tenant, tenant_policies, user, and
// role_assignment rows were all created with correct linkage.
func TestBootstrap_FreshDB(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	cfg := baseConfig(authDSN, tenantDSN)
	var buf bytes.Buffer

	err := bootstrap.RunWithConfig(ctx, cfg, "supersecretpassword1", &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Bootstrap complete.")
	assert.Contains(t, out, "Tenant name:   acme")
	assert.Contains(t, out, "Admin email:   admin@example.com")

	// ── Verify tenant rows in tenant DB ──────────────────────────────────────

	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	require.NoError(t, err)
	defer tenantPool.Close()

	var tenantName, tenantPlan string
	err = tenantPool.QueryRow(ctx,
		`SELECT name, plan FROM tenants WHERE name = $1`, "acme",
	).Scan(&tenantName, &tenantPlan)
	require.NoError(t, err, "tenant row not found")
	assert.Equal(t, "acme", tenantName)
	assert.Equal(t, "standard", tenantPlan)

	// tenant_policies row must exist with defaults.
	var policyCount int
	err = tenantPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenant_policies
		 WHERE tenant_id = (SELECT id FROM tenants WHERE name = $1)`,
		"acme",
	).Scan(&policyCount)
	require.NoError(t, err)
	assert.Equal(t, 1, policyCount, "tenant_policies row missing")

	// deployment_metadata sentinel must have been recorded.
	var metaValue string
	err = tenantPool.QueryRow(ctx,
		`SELECT value::text FROM deployment_metadata WHERE key = 'bootstrap_tenant_id'`,
	).Scan(&metaValue)
	require.NoError(t, err, "deployment_metadata row not found")
	assert.NotEmpty(t, strings.Trim(metaValue, `"`), "bootstrap_tenant_id value is empty")

	// ── Verify user + role in auth DB ────────────────────────────────────────

	authPool, err := pgxpool.New(ctx, authDSN)
	require.NoError(t, err)
	defer authPool.Close()

	var (
		userEmail    string
		userUsername string
		userKind     string
		userStatus   string
	)
	err = authPool.QueryRow(ctx,
		`SELECT email, username, kind, status FROM users WHERE email = $1`,
		"admin@example.com",
	).Scan(&userEmail, &userUsername, &userKind, &userStatus)
	require.NoError(t, err, "user row not found in auth DB")
	assert.Equal(t, "admin@example.com", userEmail)
	assert.Equal(t, "bootstrapadmin", userUsername)
	assert.Equal(t, "human", userKind)
	assert.Equal(t, "active", userStatus)

	// REDESIGN-001 Phase 5.1: bootstrap must set is_global_admin=true on the
	// admin user row directly instead of inserting a legacy (admin, org, '*')
	// role_assignments marker. Verify the typed column is set.
	var isGlobalAdmin bool
	err = authPool.QueryRow(ctx,
		`SELECT is_global_admin FROM users WHERE email = $1`,
		"admin@example.com",
	).Scan(&isGlobalAdmin)
	require.NoError(t, err, "user row not found when checking is_global_admin")
	assert.True(t, isGlobalAdmin, "bootstrap admin must have is_global_admin=true (REDESIGN-001 Phase 5.1)")

	// Confirm no legacy (admin, org, '*') role_assignment was inserted — the
	// typed column replaces it entirely and the old marker row is no longer
	// written.
	var raCount int
	err = authPool.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM role_assignments ra
		 JOIN roles r ON r.id = ra.role_id
		 JOIN users u ON u.id = ra.user_id
		 WHERE u.email = $1
		   AND r.name        = 'admin'
		   AND ra.scope_type  = 'org'
		   AND ra.scope_value = '*'`,
		"admin@example.com",
	).Scan(&raCount)
	require.NoError(t, err)
	assert.Equal(t, 0, raCount, "bootstrap must NOT insert the legacy (admin, org, '*') marker after Phase 5.1")
}

// TestBootstrap_SecondCallSameAdmin_Fails verifies that a second bootstrap
// call for the same tenant + email returns a *ValidationError (admin already
// exists). Exit-code 2 territory.
func TestBootstrap_SecondCallSameAdmin_Fails(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	tenantID := uuid.New()
	cfg := baseConfig(authDSN, tenantDSN)
	cfg.TenantID = tenantID

	// First call — must succeed.
	err := bootstrap.RunWithConfig(ctx, cfg, "firstpassword1!", &bytes.Buffer{})
	require.NoError(t, err, "first bootstrap should succeed")

	// Second call with the same admin email + tenant ID — must fail.
	err = bootstrap.RunWithConfig(ctx, cfg, "secondpassword2!", &bytes.Buffer{})
	require.Error(t, err)

	var verr *bootstrap.ValidationError
	require.ErrorAs(t, err, &verr, "expected a *ValidationError, got %T: %v", err, err)
	assert.Contains(t, verr.Error(), "admin already exists")
}

// TestBootstrap_SingleMode_DifferentTenant_Fails verifies that running a
// second bootstrap in DEPLOYMENT_MODE=single with a DIFFERENT tenant ID is
// refused with a *ValidationError.
func TestBootstrap_SingleMode_DifferentTenant_Fails(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	firstTenantID := uuid.New()
	cfg := baseConfig(authDSN, tenantDSN)
	cfg.TenantID = firstTenantID
	cfg.DeploymentMode = "single"

	// First call — must succeed.
	err := bootstrap.RunWithConfig(ctx, cfg, "password1single!", &bytes.Buffer{})
	require.NoError(t, err, "first bootstrap should succeed")

	// Second call with a DIFFERENT tenant ID — must fail.
	cfg2 := cfg
	cfg2.TenantID = uuid.New()
	cfg2.AdminEmail = "admin2@example.com"
	cfg2.AdminUsername = "admin2user"
	cfg2.TenantName = "betacorp"

	err = bootstrap.RunWithConfig(ctx, cfg2, "password2single!", &bytes.Buffer{})
	require.Error(t, err)

	var verr *bootstrap.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Contains(t, verr.Error(), "already bootstrapped")
}

// TestBootstrap_MultiMode_DifferentTenant_Succeeds verifies that
// DEPLOYMENT_MODE=multi allows additional tenants past the first bootstrap.
func TestBootstrap_MultiMode_DifferentTenant_Succeeds(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	cfg1 := baseConfig(authDSN, tenantDSN)
	cfg1.TenantID = uuid.New()
	cfg1.DeploymentMode = "multi"

	// First bootstrap — must succeed.
	err := bootstrap.RunWithConfig(ctx, cfg1, "password1multi!", &bytes.Buffer{})
	require.NoError(t, err, "first multi-mode bootstrap should succeed")

	// Second bootstrap with a DIFFERENT tenant — must also succeed.
	cfg2 := bootstrap.Config{
		AdminEmail:     "admin2@example.com",
		AdminUsername:  "admin2user",
		TenantName:     "betacorp",
		TenantID:       uuid.New(),
		AuthDBDSN:      authDSN,
		TenantDBDSN:    tenantDSN,
		DeploymentMode: "multi",
	}

	var buf bytes.Buffer
	err = bootstrap.RunWithConfig(ctx, cfg2, "password2multi!", &buf)
	require.NoError(t, err, "second multi-mode bootstrap should succeed")
	assert.Contains(t, buf.String(), "Bootstrap complete.")

	// Both tenants must exist (plus the seed dev tenant from migration).
	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	require.NoError(t, err)
	defer tenantPool.Close()

	var count int
	// Count only the two bootstrap tenants (exclude the dev-seed row).
	err = tenantPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenants WHERE name IN ('acme', 'betacorp')`,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "expected 2 bootstrap tenant rows after two multi-mode bootstraps")
}

// TestBootstrap_PasswordFromStdin verifies that the password is correctly
// stored as an argon2id hash and that argon2.Verify round-trips correctly.
// Uses RunWithConfig (no real stdin) but exercises the full hash + verify path.
func TestBootstrap_PasswordFromStdin(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	cfg := baseConfig(authDSN, tenantDSN)
	const rawPassword = "correct-horse-battery-staple"

	err := bootstrap.RunWithConfig(ctx, cfg, rawPassword, &bytes.Buffer{})
	require.NoError(t, err)

	// Read the stored hash and verify it against the raw password.
	authPool, err := pgxpool.New(ctx, authDSN)
	require.NoError(t, err)
	defer authPool.Close()

	var storedHash string
	err = authPool.QueryRow(ctx,
		`SELECT password_hash FROM users WHERE email = $1`, cfg.AdminEmail,
	).Scan(&storedHash)
	require.NoError(t, err, "user row not found in auth DB")

	ok, err := argon2pkg.Verify(rawPassword, storedHash)
	require.NoError(t, err, "Verify returned an error (malformed hash?)")
	assert.True(t, ok, "stored hash does not verify against the supplied password")
}

// TestBootstrap_EmptyPasswordRejected verifies that an empty password returns
// a *ValidationError (exit code 2 territory), not a panic or DB error.
func TestBootstrap_EmptyPasswordRejected(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	cfg := baseConfig(authDSN, tenantDSN)

	err := bootstrap.RunWithConfig(ctx, cfg, "", &bytes.Buffer{})
	require.Error(t, err)

	var verr *bootstrap.ValidationError
	require.ErrorAs(t, err, &verr, "expected a *ValidationError for empty password, got %T: %v", err, err)
	assert.Contains(t, verr.Error(), "empty")
}

// TestBootstrap_SetsIsGlobalAdmin is a focused test for REDESIGN-001 Phase 5.1:
// it verifies that writeAdmin sets is_global_admin=true on the users row and
// does NOT insert the deprecated (admin, org, '*') role_assignments marker.
//
// This is the definitive database-level assertion for the typed column contract.
// It supplements the broader assertions already in TestBootstrap_FreshDB.
func TestBootstrap_SetsIsGlobalAdmin(t *testing.T) {
	ctx := context.Background()
	authDSN, tenantDSN := setupDatabases(t)

	cfg := baseConfig(authDSN, tenantDSN)
	cfg.AdminEmail = "admin-pga5@example.com"
	cfg.AdminUsername = "pga5admin"

	err := bootstrap.RunWithConfig(ctx, cfg, "Str0ng!Phase5.1Password", &bytes.Buffer{})
	require.NoError(t, err, "bootstrap should succeed")

	authPool, err := pgxpool.New(ctx, authDSN)
	require.NoError(t, err)
	defer authPool.Close()

	// ── Typed column must be set ────────────────────────────────────────────────

	var isGlobalAdmin bool
	err = authPool.QueryRow(ctx,
		`SELECT is_global_admin FROM users WHERE email = $1`,
		cfg.AdminEmail,
	).Scan(&isGlobalAdmin)
	require.NoError(t, err, "admin user not found in auth DB")
	assert.True(t, isGlobalAdmin,
		"REDESIGN-001 Phase 5.1: bootstrap must set users.is_global_admin=true on the first admin")

	// ── Legacy (admin, org, '*') marker must NOT exist ─────────────────────────

	var markerCount int
	err = authPool.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM role_assignments ra
		 JOIN roles r ON r.id = ra.role_id
		 JOIN users u ON u.id = ra.user_id
		 WHERE u.email       = $1
		   AND r.name        = 'admin'
		   AND ra.scope_type  = 'org'
		   AND ra.scope_value = '*'`,
		cfg.AdminEmail,
	).Scan(&markerCount)
	require.NoError(t, err)
	assert.Equal(t, 0, markerCount,
		"REDESIGN-001 Phase 5.1: bootstrap must NOT insert the (admin, org, '*') legacy marker — is_global_admin column replaces it")
}
