//go:build integration

// Package containers provides testcontainers helpers for integration tests.
// This file adds auth_with_audit.go — a compound helper that boots both
// auth-postgres and audit-postgres testcontainers.
package containers

import (
	"context"
	"fmt"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
)

// Bundle holds the resources returned by NewAuthWithAudit. Callers must call
// Cleanup when they are done to release containers and connections.
//
// AuditConn is always nil in this implementation: the audit gRPC server cannot
// be wired in-process inside libs/ because CLAUDE.md §5 prohibits libs/ from
// importing services/. T19 callers that need an audit handler should construct
// one directly from the AuditPool using services/audit/internal/handler.NewGRPC.
//
// TODO(FE-API-048 T19): if a future task moves the audit handler interface
// into libs/ (or proto/gen/go), remove this TODO and wire AuditConn via bufconn
// following the services/core/internal/handler pattern.
type Bundle struct {
	// AuthPool is a connection pool pointed at the auth-postgres container with
	// all auth migrations already applied.
	AuthPool *pgxpool.Pool
	// AuditPool is a connection pool pointed at the audit-postgres container
	// with all audit migrations already applied. The pool's AfterConnect hook
	// issues SET ROLE registry_audit_app so any query runs under the correct
	// low-privilege role (SEC-001).
	AuditPool *pgxpool.Pool
	// AuditConn is a gRPC client connection to an in-process audit service via
	// bufconn. Currently nil — see struct-level TODO above.
	AuditConn *grpc.ClientConn
	// Cleanup tears down both containers and closes the connection pools.
	// Callers should defer Cleanup immediately after receiving the Bundle.
	Cleanup func()
}

// AuthWithAuditOpts carries caller-supplied migration filesystems. The libs/
// package cannot import services/ (CLAUDE.md §5), so callers must provide the
// embed.FS values from the service migration packages.
//
// Example:
//
//	opts := containers.AuthWithAuditOpts{
//	    AuthMigrations:  authmigrations.FS,
//	    AuditMigrations: auditmigrations.FS,
//	}
//	bundle := containers.NewAuthWithAudit(t, ctx, opts)
type AuthWithAuditOpts struct {
	// AuthMigrations is the embed.FS containing auth service SQL migration files
	// (from services/auth/migrations). Must be non-nil.
	AuthMigrations fs.FS
	// AuditMigrations is the embed.FS containing audit service SQL migration
	// files (from services/audit/migrations). Must be non-nil.
	AuditMigrations fs.FS
}

// NewAuthWithAudit starts two independent PostgreSQL 16 containers — one for
// the auth schema, one for the audit schema — applies all migrations on each,
// and returns a Bundle with ready-to-use connection pools.
//
// The caller must supply migration filesystems via opts because libs/ cannot
// import services/ (CLAUDE.md §5). See AuthWithAuditOpts for the expected
// values.
//
// The function is intended for integration tests that need both auth and audit
// state (for example, activity-facade tests that assert audit_events rows are
// visible through the ActivityService). It follows the same helper pattern as
// Postgres(t) and Redis(t): t.Cleanup is wired automatically.
func NewAuthWithAudit(t testing.TB, ctx context.Context, opts AuthWithAuditOpts) *Bundle {
	t.Helper()

	if opts.AuthMigrations == nil {
		t.Fatal("auth_with_audit: AuthWithAuditOpts.AuthMigrations must not be nil")
	}
	if opts.AuditMigrations == nil {
		t.Fatal("auth_with_audit: AuthWithAuditOpts.AuditMigrations must not be nil")
	}

	// ── Auth postgres ─────────────────────────────────────────────────────────

	authDSN, authStop, err := PostgresNoT(ctx)
	if err != nil {
		t.Fatalf("auth_with_audit: start auth postgres: %v", err)
	}

	authPool, err := pgxpool.New(ctx, authDSN)
	if err != nil {
		authStop()
		t.Fatalf("auth_with_audit: connect auth pool: %v", err)
	}

	if err := applyMigrations(authDSN, opts.AuthMigrations); err != nil {
		authPool.Close()
		authStop()
		t.Fatalf("auth_with_audit: run auth migrations: %v", err)
	}

	// ── Audit postgres ────────────────────────────────────────────────────────

	auditDSN, auditStop, err := PostgresNoT(ctx)
	if err != nil {
		authPool.Close()
		authStop()
		t.Fatalf("auth_with_audit: start audit postgres: %v", err)
	}

	// Apply audit migrations before building the runtime pool so that the
	// registry_audit_app role (created inside the migration SQL) and the
	// GRANT registry_audit_app TO CURRENT_USER statement are in place before
	// AfterConnect tries to SET ROLE.
	if err := applyMigrations(auditDSN, opts.AuditMigrations); err != nil {
		authPool.Close()
		authStop()
		auditStop()
		t.Fatalf("auth_with_audit: run audit migrations: %v", err)
	}

	// Build the runtime audit pool with the AfterConnect hook that elevates
	// every connection to the low-privilege role required by the audit RLS
	// policies (SEC-001). Without this hook, queries run as the schema owner
	// and FORCE ROW LEVEL SECURITY silently exposes all tenants' data.
	auditPoolCfg, err := pgxpool.ParseConfig(auditDSN)
	if err != nil {
		authPool.Close()
		authStop()
		auditStop()
		t.Fatalf("auth_with_audit: parse audit pool config: %v", err)
	}
	auditPoolCfg.AfterConnect = func(connCtx context.Context, conn *pgx.Conn) error {
		_, setErr := conn.Exec(connCtx, "SET ROLE registry_audit_app")
		return setErr
	}
	auditPool, err := pgxpool.NewWithConfig(ctx, auditPoolCfg)
	if err != nil {
		authPool.Close()
		authStop()
		auditStop()
		t.Fatalf("auth_with_audit: connect audit pool: %v", err)
	}

	// ── Cleanup ───────────────────────────────────────────────────────────────

	cleanup := func() {
		auditPool.Close()
		authPool.Close()
		auditStop()
		authStop()
	}
	t.Cleanup(cleanup)

	return &Bundle{
		AuthPool:  authPool,
		AuditPool: auditPool,
		// AuditConn is intentionally nil — see struct-level TODO above.
		AuditConn: nil,
		Cleanup:   cleanup,
	}
}

// applyMigrations opens a short-lived database/sql connection from dsn, sets
// the goose base FS to the given embed.FS, and runs all Up migrations. It is
// extracted as a helper so each call site does not repeat the parse-config /
// stdlib.OpenDB / goose boilerplate.
func applyMigrations(dsn string, migrations fs.FS) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	return goose.Up(sqlDB, ".")
}
