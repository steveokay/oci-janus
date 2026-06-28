//go:build integration

// Package service — Activity facade integration test (FE-API-048, Task 19).
//
// TestIntegration_ActivityFacade_EndToEnd boots two real PostgreSQL containers
// (auth-postgres and audit-postgres via the T18 Bundle), seeds a service account
// and its shadow user in the auth DB, inserts three audit events in the audit DB,
// then calls ActivityService.List and asserts that all three events round-trip
// correctly through the gRPC path.
//
// Audit migrations are provided inline via testing/fstest.MapFS so this test
// does not need to import services/audit/migrations — that import would add
// services/audit to services/auth's go.mod and violate the constraint that
// go mod tidy produces no diff. The SQL content is copied verbatim from
// services/audit/migrations/*.sql and must be kept in sync if those files change.
//
// The audit gRPC server is wired in-process via bufconn so the test does not
// need a network socket or an external audit service process. The server
// implements only GetNotifications using direct SQL against bundle.AuditPool;
// all other AuditServiceServer methods return Unimplemented (the ActivityService
// only ever calls GetNotifications).
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authtestutil "github.com/steveokay/oci-janus/services/auth/internal/testutil"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

// ── Inline audit migrations ───────────────────────────────────────────────────
//
// These SQL files are copied verbatim from services/audit/migrations/*.sql.
// They are embedded inline here using testing/fstest.MapFS so this test file
// does not need to import services/audit (which would modify go.mod).
//
// If services/audit/migrations/*.sql change, update the corresponding entries
// here.

// auditMigrationsFS provides the three audit migration files as an in-memory
// fs.FS that goose can consume via containers.AuthWithAuditOpts.AuditMigrations.
var auditMigrationsFS = func() fstest.MapFS {
	// Migration 1: create audit_events partitioned table + indexes + no-update rule.
	const m1 = `-- +goose Up

CREATE TABLE audit_events (
    id          UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    actor_id    TEXT        NOT NULL,
    actor_type  TEXT        NOT NULL
                            CHECK (actor_type IN ('user', 'robot', 'system')),
    actor_ip    TEXT        NOT NULL DEFAULT '',
    action      TEXT        NOT NULL,
    resource    TEXT        NOT NULL DEFAULT '',
    outcome     TEXT        NOT NULL
                            CHECK (outcome IN ('success', 'failure')),
    metadata    JSONB       NOT NULL DEFAULT '{}',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

-- Default partition covering the first year.
CREATE TABLE audit_events_default PARTITION OF audit_events DEFAULT;

-- Append-only rules.
CREATE RULE no_update_audit AS ON UPDATE TO audit_events DO INSTEAD NOTHING;
CREATE RULE no_delete_audit AS ON DELETE TO audit_events DO INSTEAD NOTHING;

CREATE INDEX idx_audit_events_tenant_occurred ON audit_events(tenant_id, occurred_at DESC);
CREATE INDEX idx_audit_events_actor           ON audit_events(actor_id, occurred_at DESC);
CREATE INDEX idx_audit_events_action          ON audit_events(action, occurred_at DESC);

-- +goose Down

DROP TABLE IF EXISTS audit_events CASCADE;
`

	// Migration 2: registry_audit_app role + RLS.
	const m2 = `-- +goose Up
-- +goose StatementBegin

CREATE ROLE registry_audit_app NOLOGIN;
GRANT registry_audit_app TO CURRENT_USER;
GRANT INSERT, SELECT ON audit_events TO registry_audit_app;
GRANT DELETE ON audit_events_default TO registry_audit_app;

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;

CREATE POLICY audit_insert ON audit_events
    AS PERMISSIVE FOR INSERT
    WITH CHECK (true);

CREATE POLICY audit_select ON audit_events
    AS PERMISSIVE FOR SELECT
    USING (true);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS audit_select ON audit_events;
DROP POLICY IF EXISTS audit_insert ON audit_events;
ALTER TABLE audit_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_events DISABLE ROW LEVEL SECURITY;
REVOKE DELETE ON audit_events_default FROM registry_audit_app;
REVOKE INSERT, SELECT ON audit_events FROM registry_audit_app;
REVOKE registry_audit_app FROM CURRENT_USER;
DROP ROLE IF EXISTS registry_audit_app;

-- +goose StatementEnd
`

	// Migration 3: repo_activity index (optional for this test, but keeps the
	// migration sequence consistent with the production schema).
	const m3 = `-- +goose Up
-- +goose StatementBegin

CREATE INDEX idx_audit_events_repo_activity
    ON audit_events (
        tenant_id,
        (metadata->'raw'->>'repository_name'),
        occurred_at DESC,
        id DESC
    )
    WHERE metadata->'raw'->>'repository_name' IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_audit_events_repo_activity;

-- +goose StatementEnd
`

	return fstest.MapFS{
		"20240101000001_create_audit_events.sql": &fstest.MapFile{Data: []byte(m1)},
		"20240101000002_audit_rls_role.sql":      &fstest.MapFile{Data: []byte(m2)},
		"20260619120000_repo_activity_index.sql": &fstest.MapFile{Data: []byte(m3)},
	}
}()

// ── Minimal in-process AuditService server ───────────────────────────────────

// inProcessAuditServer implements auditv1.AuditServiceServer backed by direct
// SQL queries against the audit-postgres container. Only GetNotifications is
// implemented; all other methods return Unimplemented so unexpected calls fail
// loudly during the test.
//
// The implementation mirrors the core of services/audit/internal/repository
// GetNotifications but deliberately omits the action-type allowlist filter and
// keyset pagination — for this test we only insert a small number of rows and
// we want all three to be visible so ActivityService.List can apply its
// actor_id filter.
type inProcessAuditServer struct {
	auditv1.UnimplementedAuditServiceServer
	// pool is the authenticated runtime pool; AfterConnect SET ROLE
	// registry_audit_app is applied by NewAuthWithAudit (SEC-001).
	pool *pgxpool.Pool
}

// GetNotifications returns all audit events for the given tenant ordered
// newest-first. since/limit/page_token/event_types are intentionally ignored
// because the test's 3-row dataset does not need paging or filtering.
func (s *inProcessAuditServer) GetNotifications(
	ctx context.Context,
	req *auditv1.GetNotificationsRequest,
) (*auditv1.GetNotificationsResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Return all events for the tenant, newest-first. The 200-row cap is a
	// safety guard; the test only inserts 4 rows so it is never reached.
	rows, err := s.pool.Query(ctx,
		`SELECT id, actor_id, action, metadata, occurred_at
		 FROM audit_events
		 WHERE tenant_id = $1
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT 200`,
		tenantID,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query audit events: %v", err)
	}
	defer rows.Close()

	var notifications []*auditv1.NotificationEvent
	for rows.Next() {
		var (
			id         uuid.UUID
			actorID    string
			action     string
			metadata   []byte
			occurredAt time.Time
		)
		if err := rows.Scan(&id, &actorID, &action, &metadata, &occurredAt); err != nil {
			return nil, status.Errorf(codes.Internal, "scan audit row: %v", err)
		}

		// Parse the stored metadata JSON into the string map that
		// ActivityService.trimNotifications reads (meta["repo"], meta["source_ip"],
		// meta["api_key_id"], meta["outcome"]). seedAuditEvent writes exactly this
		// shape directly into the JSONB column.
		var metaMap map[string]string
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &metaMap)
		}
		if metaMap == nil {
			metaMap = map[string]string{}
		}

		notifications = append(notifications, &auditv1.NotificationEvent{
			EventId:    id.String(),
			EventType:  action,
			OccurredAt: timestamppb.New(occurredAt),
			ActorId:    actorID,
			Metadata:   metaMap,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate audit rows: %v", err)
	}

	return &auditv1.GetNotificationsResponse{
		Notifications: notifications,
		UnreadCount:   int32(len(notifications)),
	}, nil
}

// ── bufconn wiring ────────────────────────────────────────────────────────────

const bufSize = 1024 * 1024 // 1 MiB in-memory buffer for bufconn

// startAuditBufconn starts an in-process gRPC server backed by auditSrv via
// bufconn, returns a *grpc.ClientConn wired to it, and registers a t.Cleanup
// to stop the server gracefully when the test ends. No TCP socket or TLS is
// needed because the connection is in-memory and stays within the test process.
func startAuditBufconn(t testing.TB, auditSrv auditv1.AuditServiceServer) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(grpcSrv, auditSrv)

	// Serve in the background; errors after GracefulStop closes the listener
	// are expected and discarded.
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("startAuditBufconn: grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		grpcSrv.GracefulStop()
		_ = conn.Close()
		_ = lis.Close()
	})

	return conn
}

// ── Audit event seeder ────────────────────────────────────────────────────────

// seedAuditEvent inserts a single audit_events row directly into the audit DB.
// Production writes flow through the RabbitMQ event consumer; direct SQL is
// the correct approach in tests where no broker is available.
//
// Metadata is stored as a flat string map matching what ActivityService.
// trimNotifications reads (meta["repo"], meta["source_ip"], etc.).
// offsetSecs back-dates occurred_at so staggered inserts produce a stable
// newest-first order even when multiple inserts land in the same millisecond.
func seedAuditEvent(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID uuid.UUID,
	actorID uuid.UUID,
	action string,
	repo string,
	offsetSecs int,
) {
	t.Helper()

	// Flat metadata map; keys match what trimNotifications reads.
	meta := map[string]string{
		"repo":       repo,
		"source_ip":  "10.0.0.1",
		"api_key_id": "test-key",
		"outcome":    "success",
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("seedAuditEvent: marshal metadata: %v", err)
	}

	// Stagger events so ORDER BY occurred_at DESC is deterministic.
	occurredAt := time.Now().Add(-time.Duration(offsetSecs) * time.Second)

	_, err = pool.Exec(ctx,
		`INSERT INTO audit_events
		    (tenant_id, actor_id, actor_type, actor_ip, action, resource, outcome, metadata, occurred_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tenantID,
		actorID.String(), // actor_id is TEXT in the audit schema
		"user",           // actor_type CHECK (user | robot | system)
		"10.0.0.1",       // actor_ip
		action,
		fmt.Sprintf("repo:%s", repo), // resource column
		"success",                    // outcome CHECK (success | failure)
		metaJSON,                     // metadata JSONB
		occurredAt,
	)
	if err != nil {
		t.Fatalf("seedAuditEvent: insert %q for actor %s: %v", action, actorID, err)
	}
}

// ── Integration test ──────────────────────────────────────────────────────────

// TestIntegration_ActivityFacade_EndToEnd validates the ActivityService.List
// path end-to-end with real PostgreSQL containers:
//
//  1. Boots auth-postgres + audit-postgres via the T18 Bundle.
//  2. Seeds a service account and its shadow user in the auth DB.
//  3. Inserts 3 audit events (push.image, pull.image, auth.token_issued) for
//     the shadow user plus one noise event for a different actor.
//  4. Calls ActivityService.List with CallerIsAdmin=true targeting the shadow user.
//  5. Asserts the returned slice has length 3, all three actions are present, and
//     source_ip/repo/api_key_id/outcome metadata round-tripped correctly.
func TestIntegration_ActivityFacade_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// ── 1. Boot auth-postgres and audit-postgres ───────────────────────────────

	bundle := containers.NewAuthWithAudit(t, ctx, containers.AuthWithAuditOpts{
		AuthMigrations:  authmigrations.FS,
		AuditMigrations: auditMigrationsFS,
	})
	// bundle.Cleanup is already registered with t.Cleanup inside NewAuthWithAudit;
	// defer here as belt-and-suspenders for early returns from t.Fatal.
	defer bundle.Cleanup()

	// ── 2. Seed a service account and its shadow user in the auth DB ───────────

	// Fresh tenant UUID so this test's data is isolated from other tests.
	tenantID := uuid.New()

	// callerAdminID is a synthetic UUID for the admin caller.
	// CallerIsAdmin=true in ListActivityOpts bypasses the DB lookup for the
	// caller, so no row seeding is needed for the admin itself.
	callerAdminID := uuid.New()

	saRepo := repository.NewServiceAccountRepo(bundle.AuthPool)
	userRepo := repository.NewUserRepository(bundle.AuthPool)

	// NewServiceAccount creates the SA + shadow user in one atomic transaction
	// and returns the shadow user ID. ActivityService.GetUserAnyKind will look
	// up this row to resolve the target principal.
	_, shadowID := authtestutil.NewServiceAccount(
		t, ctx,
		saRepo, userRepo,
		tenantID,
		"ci-prod",
		"pull", "push",
	)

	// ── 3. Insert 3 audit events for the shadow user + 1 noise event ──────────

	// Stagger occurred_at (newest first: offsetSecs 0 < 1 < 2) so the
	// ORDER BY occurred_at DESC sequence in inProcessAuditServer is deterministic.
	seedAuditEvent(t, ctx, bundle.AuditPool, tenantID, shadowID, "push.image", "myorg/myrepo:1.0", 2)
	seedAuditEvent(t, ctx, bundle.AuditPool, tenantID, shadowID, "pull.image", "myorg/myrepo:1.0", 1)
	seedAuditEvent(t, ctx, bundle.AuditPool, tenantID, shadowID, "auth.token_issued", "", 0)

	// One noise event belonging to a different actor; trimNotifications must
	// drop this and return only the shadow user's events.
	otherActor := uuid.New()
	seedAuditEvent(t, ctx, bundle.AuditPool, tenantID, otherActor, "push.image", "other/repo", 3)

	// ── 4. Wire audit gRPC via bufconn and construct ActivityService ───────────

	// Start an in-process AuditService server backed by bundle.AuditPool.
	// The pool's AfterConnect hook already SET ROLE registry_audit_app so all
	// queries run under the low-privilege role (SEC-001 / T18 contract).
	auditSrv := &inProcessAuditServer{pool: bundle.AuditPool}
	conn := startAuditBufconn(t, auditSrv)
	auditClient := auditv1.NewAuditServiceClient(conn)

	// ActivityService is wired with the real UserRepository (auth DB) and the
	// real audit gRPC client (in-process server backed by audit DB).
	svc := NewActivityService(userRepo, auditClient)

	// ── 5. Assert ActivityService.List returns exactly the 3 shadow-user events ─

	activities, nextToken, err := svc.List(ctx, ListActivityOpts{
		CallerUserID:   callerAdminID,
		CallerTenantID: tenantID,
		CallerIsAdmin:  true, // admin path bypasses self-only guard
		TargetUserID:   shadowID,
		PageSize:       10,
	})
	require.NoError(t, err, "ActivityService.List must succeed for a valid admin + shadow user")
	require.Empty(t, nextToken, "no pagination cursor expected for a 3-row result set")
	require.Len(t, activities, 3,
		"exactly 3 events must be returned; the noise event for a different actor must be filtered out")

	// Collect returned actions for order-independent assertion.
	actionSet := make(map[string]struct{}, len(activities))
	for _, a := range activities {
		actionSet[a.Action] = struct{}{}
	}
	require.Contains(t, actionSet, "push.image", "push.image event must be present")
	require.Contains(t, actionSet, "pull.image", "pull.image event must be present")
	require.Contains(t, actionSet, "auth.token_issued", "auth.token_issued event must be present")

	// Verify push.image metadata round-tripped through the DB and gRPC layer.
	var pushActivity *PrincipalActivity
	for i := range activities {
		if activities[i].Action == "push.image" {
			pushActivity = &activities[i]
			break
		}
	}
	require.NotNil(t, pushActivity, "push.image activity must be findable in the returned slice")
	require.Equal(t, "myorg/myrepo:1.0", pushActivity.Repo,
		"repo metadata must survive the DB→gRPC→ActivityService round-trip")
	require.Equal(t, "10.0.0.1", pushActivity.SourceIP,
		"source_ip metadata must survive the DB→gRPC→ActivityService round-trip")
	require.Equal(t, "test-key", pushActivity.APIKeyID,
		"api_key_id metadata must survive the DB→gRPC→ActivityService round-trip")
	require.Equal(t, "success", pushActivity.Status,
		"outcome metadata must survive the DB→gRPC→ActivityService round-trip")
	require.False(t, pushActivity.At.IsZero(),
		"occurred_at must be non-zero after round-tripping through the audit DB")
}
