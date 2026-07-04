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

	res, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.Intact() {
		t.Fatalf("expected intact chain, got tampered row id=%s at=%s", res.FirstBadID, res.FirstBadAt)
	}
	if res.Unverifiable != 0 {
		t.Fatalf("expected 0 unverifiable rows in a fresh chain, got %d", res.Unverifiable)
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
	if res, err := repo.VerifyChain(ctx, tenant); err != nil || !res.Intact() {
		t.Fatalf("expected intact pre-tamper, got id=%s err=%v", res.FirstBadID, err)
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

	res, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain post-tamper: %v", err)
	}
	if res.FirstBadID != ids[1] {
		t.Fatalf("expected verifier to flag middle row id=%s, got %s", ids[1], res.FirstBadID)
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
	res, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.Intact() {
		t.Fatalf("concurrent inserts produced a broken chain, first bad id=%s", res.FirstBadID)
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

	res, err := repo.VerifyChain(ctx, uuid.New())
	if err != nil {
		t.Fatalf("VerifyChain empty: %v", err)
	}
	if !res.Intact() {
		t.Fatalf("expected intact empty chain, got id=%s", res.FirstBadID)
	}
}

// TestHashChain_preChainRowsReportedUnverifiable simulates a deployment that
// already held audit rows BEFORE the Phase 6.12 hash-chain migration. That
// migration backfills every pre-existing row with the transient row_hash
// DEFAULT — a single 0x00 byte — which also matches the genesis prev_hash
// sentinel. A naive verifier would bucket those rows against the genuine
// genesis row and misreport a fork. VerifyChain must instead count them into
// Unverifiable and still verify the real chain layered on top (SEC-051).
func TestHashChain_preChainRowsReportedUnverifiable(t *testing.T) {
	repo, pool := newRepoWithPool(t)
	tenant := uuid.New()
	ctx := context.Background()

	// Seed two raw "pre-migration" rows directly into the default partition,
	// bypassing the app inserter, each carrying the pre-chain sentinel
	// (prev_hash = row_hash = 0x00). Inserted first, they take the lowest
	// chain_seq values — exactly the ordering a real backfill produces.
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO audit_events_default
			   (tenant_id, actor_id, actor_type, action, outcome, occurred_at, prev_hash, row_hash)
			 VALUES ($1, 'legacy', 'system', 'legacy.event', 'success', $2, decode('00','hex'), decode('00','hex'))`,
			tenant, base.Add(time.Duration(i)*time.Second),
		); err != nil {
			t.Fatalf("seed pre-chain row #%d: %v", i, err)
		}
	}

	// Now insert three real rows via the app inserter. The first insert's tip
	// query returns a pre-chain row's row_hash (0x00), so it chains off the
	// genesis sentinel just as a fresh deployment's first row would.
	for i := 0; i < 3; i++ {
		ev := makeEvent(tenant, "push.image", base.Add(time.Duration(10+i)*time.Second))
		if err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}

	res, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.Intact() {
		t.Fatalf("real chain built atop pre-chain rows must verify, got bad id=%s at=%s", res.FirstBadID, res.FirstBadAt)
	}
	if res.Unverifiable != 2 {
		t.Fatalf("expected the 2 pre-chain rows reported as unverifiable, got %d", res.Unverifiable)
	}
}

// TestHashChain_zeroedRowHashNotLaunderedAsUnverifiable pins SEC-NEW-1: a
// DB-level actor with out-of-band UPDATE (outside the INSERT-only role model
// the chain is written against) must not be able to zero a *chained* row's
// row_hash to disguise a tamper as a mere Unverifiable bump. A genuine
// pre-chain row carries the 0x00 sentinel in BOTH prev_hash and row_hash; a
// chained row keeps its real 32-byte prev_hash, so zeroing only its row_hash
// leaves it in the walk where the recompute mismatch flags it as tampered.
func TestHashChain_zeroedRowHashNotLaunderedAsUnverifiable(t *testing.T) {
	repo, pool := newRepoWithPool(t)
	tenant := uuid.New()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)
	ids := make([]uuid.UUID, 3)
	for i := 0; i < 3; i++ {
		ev := makeEvent(tenant, "push.image", base.Add(time.Duration(i)*time.Second))
		if err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
		ids[i] = ev.ID
	}

	// Zero the tip row's row_hash directly on the default partition (bypassing
	// the no_update_audit parent rule), simulating a DB-level tamper that tries
	// to masquerade as a pre-chain row. Its prev_hash stays the real 32-byte
	// link, so the tightened guard must NOT count it as unverifiable.
	if _, err := pool.Exec(ctx,
		`UPDATE audit_events_default SET row_hash = decode('00','hex') WHERE id = $1`,
		ids[2],
	); err != nil {
		t.Fatalf("zero row_hash: %v", err)
	}

	res, err := repo.VerifyChain(ctx, tenant)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.FirstBadID != ids[2] {
		t.Fatalf("zeroed tip row must be flagged as tampered id=%s, got id=%s", ids[2], res.FirstBadID)
	}
	if res.Unverifiable != 0 {
		t.Fatalf("a zeroed chained row must NOT be counted unverifiable, got %d", res.Unverifiable)
	}
}
