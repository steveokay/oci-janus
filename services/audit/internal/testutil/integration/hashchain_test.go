//go:build integration

// REDESIGN-001 Phase 6.12 integration tests for the audit_events hash
// chain. We hit a real PostgreSQL container so we exercise the actual
// pg_advisory_xact_lock semantics, the BYTEA column round-trip, and the
// RLS policy that bans UPDATE through the parent table.
package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"golang.org/x/sync/errgroup"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	auditmigrations "github.com/steveokay/oci-janus/services/audit/migrations"
)

// newRepoWithPool spins up a Postgres container, runs migrations, and
// returns BOTH the Repository (for the inserter under test) and the raw
// pool (so the tests can issue UPDATE statements that simulate
// tampering — UPDATEs through the parent table are blocked by the
// no_update_audit RULE so the tests target the default partition
// directly, which mirrors the retention path's deletion mechanism).
func newRepoWithPool(t *testing.T) (*repository.Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(auditmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	return repository.New(pool), pool
}

// makeEvent fabricates a minimal AuditEvent suitable for hash-chain tests.
// Each call gets a unique action/resource so the canonical bytes differ
// row-to-row (a regression that hashes only constants would still link
// otherwise).
func makeEvent(tenant uuid.UUID, action string, when time.Time) *repository.AuditEvent {
	meta, _ := json.Marshal(map[string]any{"event_id": uuid.NewString(), "raw": map[string]any{"action": action}})
	return &repository.AuditEvent{
		TenantID:   tenant,
		ActorID:    "alice",
		ActorType:  "user",
		ActorIP:    "10.0.0.1",
		Action:     action,
		Resource:   "myorg/myrepo:" + action,
		Outcome:    "success",
		Metadata:   meta,
		OccurredAt: when,
	}
}

// TestHashChain_intactAfterSerialInserts verifies the happy path: three
// sequential inserts produce a chain that VerifyChain accepts.
func TestHashChain_intactAfterSerialInserts(t *testing.T) {
	repo, _ := newRepoWithPool(t)
	tenant := uuid.New()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 3; i++ {
		ev := makeEvent(tenant, "push.image", base.Add(time.Duration(i)*time.Second))
		if err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}

	badID, badTime, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if badID != uuid.Nil {
		t.Fatalf("expected intact chain, got tampered row id=%s at=%s", badID, badTime)
	}
}

// TestHashChain_detectsTamperedRow inserts three rows, then UPDATEs the
// middle row's actor_id via raw SQL (targeting the default partition
// directly to bypass the no_update_audit parent rule). VerifyChain must
// flag the tampered row's id.
func TestHashChain_detectsTamperedRow(t *testing.T) {
	repo, pool := newRepoWithPool(t)
	tenant := uuid.New()
	ctx := context.Background()

	// Insert three rows and remember the middle one's id so we can
	// assert the verifier flags exactly that row.
	base := time.Now().UTC().Truncate(time.Microsecond)
	ids := make([]uuid.UUID, 3)
	for i := 0; i < 3; i++ {
		ev := makeEvent(tenant, "push.image", base.Add(time.Duration(i)*time.Second))
		if err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
		ids[i] = ev.ID
	}

	// Sanity: chain starts intact.
	if badID, _, err := repo.VerifyChain(ctx, tenant); err != nil || badID != uuid.Nil {
		t.Fatalf("expected intact pre-tamper, got id=%s err=%v", badID, err)
	}

	// Tamper: rewrite the middle row's actor_id via the default
	// partition. We must use audit_events_default (not audit_events)
	// because the parent has a no_update_audit rule that drops
	// UPDATEs silently. This mirrors how the retention path issues
	// DELETEs against the default partition.
	if _, err := pool.Exec(ctx,
		`UPDATE audit_events_default SET actor_id = 'mallory' WHERE id = $1`,
		ids[1],
	); err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}

	badID, _, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain post-tamper: %v", err)
	}
	if badID != ids[1] {
		t.Fatalf("expected verifier to flag middle row id=%s, got %s", ids[1], badID)
	}
}

// TestHashChain_concurrentInsertsRemainIntact spawns 10 concurrent
// goroutines all inserting against the same tenant. The advisory lock
// must serialise them so all 10 rows form one continuous chain.
func TestHashChain_concurrentInsertsRemainIntact(t *testing.T) {
	repo, _ := newRepoWithPool(t)
	tenant := uuid.New()
	ctx := context.Background()

	const N = 10
	base := time.Now().UTC().Truncate(time.Microsecond)

	// Use a shared sync.Mutex purely to keep the OccurredAt values
	// monotonically increasing across goroutines — without that, two
	// rows could share a timestamp and the verifier walk would still
	// pick a deterministic order via the (occurred_at, id) secondary
	// sort, but the test reads cleaner with strictly increasing times.
	var seqMu sync.Mutex
	seq := 0

	g, gctx := errgroup.WithContext(ctx)
	for i := 0; i < N; i++ {
		g.Go(func() error {
			seqMu.Lock()
			my := seq
			seq++
			seqMu.Unlock()
			ev := makeEvent(tenant, "push.image", base.Add(time.Duration(my)*time.Millisecond))
			return repo.Insert(gctx, ev)
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent Insert: %v", err)
	}

	// All N rows must verify cleanly. A race window (e.g. two
	// goroutines both reading the same tip and producing rows with
	// the same prev_hash) would surface as a verifier mismatch on
	// whichever row was overwritten by the duplicate prev_hash.
	badID, _, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if badID != uuid.Nil {
		t.Fatalf("concurrent inserts produced a broken chain, first bad id=%s", badID)
	}

	// And confirm we actually wrote N rows for the tenant — a silent
	// failure that inserted fewer rows would also pass VerifyChain.
	rows, err := repo.Query(ctx, repository.QueryFilter{
		TenantID: tenant,
		From:     base.Add(-time.Hour),
		To:       base.Add(time.Hour),
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != N {
		t.Fatalf("expected %d rows, got %d", N, len(rows))
	}
}

// TestHashChain_emptyTenantVerifies asserts that VerifyChain on a tenant
// with zero rows returns intact — the chain is trivially valid.
func TestHashChain_emptyTenantVerifies(t *testing.T) {
	repo, _ := newRepoWithPool(t)
	ctx := context.Background()

	badID, _, err := repo.VerifyChain(ctx, uuid.New())
	if err != nil {
		t.Fatalf("VerifyChain empty: %v", err)
	}
	if badID != uuid.Nil {
		t.Fatalf("expected intact empty chain, got id=%s", badID)
	}
}
