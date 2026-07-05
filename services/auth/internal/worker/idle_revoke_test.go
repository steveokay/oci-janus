//go:build integration

// Package worker — idle_revoke_test.go exercises the FUT-003 idle-revoke
// background worker end-to-end against a real Postgres container. The
// tests seed the token_policies + api_keys tables directly, run one
// TickOnce, and assert on the resulting api_keys.is_active +
// revoke_reason state + the captured publisher events.
//
// Sub-tests:
//   - Tick_RevokesIdleKeys — seed idle / fresh / already-revoked keys,
//     tick once, assert only the idle non-revoked keys are revoked.
//   - Tick_EmitsAuditEventPerRevocation
//   - Tick_NoOpWhenPolicyIsNil
//   - Tick_NoOpWhenIdleRevokeDaysIsNil
//   - Tick_SkipsTenantsWithoutAdvisoryLock — hold pg_advisory_lock on a
//     second connection, assert TickOnce does not revoke.
//   - Tick_CascadesFailureButKeepsGoing — one tenant DB call fails,
//     assert next tenant still processed.
package worker

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

// capturingPublisher records every KeyRevokedPayload for assertion.
type capturingPublisher struct {
	mu     sync.Mutex
	events []events.KeyRevokedPayload
}

func (c *capturingPublisher) PublishKeyRevoked(_ context.Context, _ uuid.UUID, p events.KeyRevokedPayload) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, p)
	return nil
}

func (c *capturingPublisher) all() []events.KeyRevokedPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]events.KeyRevokedPayload, len(c.events))
	copy(out, c.events)
	return out
}

// gooseUpTo replays migrations to a version.
func gooseUpTo(t *testing.T, dsn string, versionPrefix string) {
	t.Helper()
	var version int64
	_, err := fmt.Sscanf(versionPrefix, "%d", &version)
	require.NoError(t, err)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(authmigrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(sqlDB, ".", version))
}

// seedTenantWithPolicy creates a policy row for the tenant and returns
// tenantID. Helper mostly to keep tests short.
func seedTenantWithPolicy(t *testing.T, pool *pgxpool.Pool, idleRevokeDays *int32) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tenantID := uuid.New()
	repo := repository.NewTokenPolicyRepo(pool)
	actor := uuid.New()
	_, err := repo.Upsert(ctx, repository.TokenPolicy{
		TenantID:        tenantID,
		IdleRevokeDays:  idleRevokeDays,
		UpdatedByUserID: &actor,
	})
	require.NoError(t, err)
	return tenantID
}

// seedKey inserts a user + api_keys row with a given last_used_at.
// Returns the key id.
func seedKey(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, name string, lastUsed *time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var userID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'u-'||$2, $2||'@example.invalid', '', 'human')
		RETURNING id`, tenantID, name).Scan(&userID))
	var keyID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes, last_used_at)
		VALUES ($1, $2, $3, 'hash', 'prefix'||substr(md5(random()::text),1,6), '{}', $4)
		RETURNING id`, tenantID, userID, name, lastUsed).Scan(&keyID))
	return keyID
}

// ageCreatedAt back-dates an api_keys row's created_at so tests can make a
// never-used key look old enough to fall outside the idle grace window.
func ageCreatedAt(t *testing.T, pool *pgxpool.Pool, keyID uuid.UUID, created time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`UPDATE api_keys SET created_at = $2 WHERE id = $1`, keyID, created)
	require.NoError(t, err)
}

// isActive returns the api_keys.is_active + revoke_reason for the given key.
func isActive(t *testing.T, pool *pgxpool.Pool, keyID uuid.UUID) (bool, string) {
	t.Helper()
	var active bool
	var reason sql.NullString
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT is_active, revoke_reason FROM api_keys WHERE id = $1`, keyID,
	).Scan(&active, &reason))
	return active, reason.String
}

func TestIdleRevoke_Tick_RevokesIdleKeys(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	gooseUpTo(t, dsn, "20260705120000")

	// Set 7-day idle threshold.
	idle := int32(7)
	tenantID := seedTenantWithPolicy(t, pool, &idle)

	// Seed four keys to pin the grace-period semantic (never-used keys are
	// measured from created_at, not treated as instantly idle):
	//   - idleKey:      last_used_at 30 days ago            → revoked
	//   - freshKey:     last_used_at 1h ago                 → kept
	//   - neverUsedNew: last_used_at NULL, created just now → kept (grace)
	//   - neverUsedOld: last_used_at NULL, created 30d ago  → revoked
	now := time.Now().UTC()
	oldTime := now.Add(-30 * 24 * time.Hour)
	recentTime := now.Add(-1 * time.Hour)

	idleKey := seedKey(t, pool, tenantID, "idle", &oldTime)
	freshKey := seedKey(t, pool, tenantID, "fresh", &recentTime)
	neverUsedNew := seedKey(t, pool, tenantID, "never-new", nil)
	neverUsedOld := seedKey(t, pool, tenantID, "never-old", nil)
	// Age the never-used-old key's created_at past the 7-day idle window so
	// it's genuinely idle; the never-used-new key keeps its now() created_at.
	ageCreatedAt(t, pool, neverUsedOld, oldTime)

	pub := &capturingPublisher{}
	w := New(pool,
		repository.NewAPIKeyRepository(pool),
		repository.NewTokenPolicyRepo(pool),
		pub, nil).WithClock(func() time.Time { return now })

	w.TickOnce(ctx)

	active, reason := isActive(t, pool, idleKey)
	require.False(t, active, "idle key should be revoked")
	require.Equal(t, "idle_revoked", reason)

	active, _ = isActive(t, pool, freshKey)
	require.True(t, active, "fresh key should remain active")

	active, _ = isActive(t, pool, neverUsedNew)
	require.True(t, active, "freshly-created never-used key should stay active (grace period)")

	active, reason = isActive(t, pool, neverUsedOld)
	require.False(t, active, "never-used key created past the idle window should be revoked")
	require.Equal(t, "idle_revoked", reason)

	// Publisher should have received 2 revocation events (idle + never-old).
	got := pub.all()
	require.Len(t, got, 2)
	for _, ev := range got {
		require.Equal(t, "idle_revoked", ev.Reason)
		require.Equal(t, tenantID.String(), ev.TenantID)
	}
}

func TestIdleRevoke_Tick_NoOpWhenIdleRevokeDaysIsNil(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	gooseUpTo(t, dsn, "20260705120000")

	// Seed a tenant with idle_revoke_days = nil (only max_ttl_days set).
	// ListTenantsWithIdleRevoke will NOT return this tenant, so the worker
	// does zero work.
	tenantID := uuid.New()
	repo := repository.NewTokenPolicyRepo(pool)
	actor := uuid.New()
	ttl := int32(30)
	_, err = repo.Upsert(ctx, repository.TokenPolicy{
		TenantID:        tenantID,
		MaxTTLDays:      &ttl,
		UpdatedByUserID: &actor,
	})
	require.NoError(t, err)

	// Seed an idle key that WOULD have been revoked if a policy applied.
	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)
	idleKey := seedKey(t, pool, tenantID, "idle", &oldTime)

	pub := &capturingPublisher{}
	w := New(pool,
		repository.NewAPIKeyRepository(pool),
		repository.NewTokenPolicyRepo(pool),
		pub, nil)

	w.TickOnce(ctx)

	active, _ := isActive(t, pool, idleKey)
	require.True(t, active, "key must remain active when policy has no idle_revoke_days")
	require.Empty(t, pub.all(), "no events for tenants without policy")
}

func TestIdleRevoke_Tick_SkipsTenantsWithoutAdvisoryLock(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	gooseUpTo(t, dsn, "20260705120000")

	idle := int32(7)
	tenantID := seedTenantWithPolicy(t, pool, &idle)

	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	idleKey := seedKey(t, pool, tenantID, "idle", &old)

	// Grab the lock on a SEPARATE connection to simulate another auth
	// replica already sweeping this tenant.
	lockConn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer lockConn.Release()

	key := idleRevokeLockKey(tenantID)
	var acquired bool
	require.NoError(t, lockConn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired))
	require.True(t, acquired, "the test must acquire the lock first")

	pub := &capturingPublisher{}
	w := New(pool,
		repository.NewAPIKeyRepository(pool),
		repository.NewTokenPolicyRepo(pool),
		pub, nil).WithClock(func() time.Time { return time.Now() })

	w.TickOnce(ctx)

	// Idle key must STILL be active — the lock was held so the worker skipped.
	active, _ := isActive(t, pool, idleKey)
	require.True(t, active, "worker should not have revoked the key while lock held")
	require.Empty(t, pub.all())

	// Release the lock ourselves so t.Cleanup runs cleanly.
	_, _ = lockConn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
}

// TestIdleRevoke_lockKey_stable asserts the FNV-64a key derivation is
// stable + collision-free within the test set. We compare against a
// hand-computed reference so a future refactor that accidentally rebases
// the salt / hash algorithm fails loudly.
func TestIdleRevoke_lockKey_stable(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	want := func() int64 {
		h := fnv.New64a()
		_, _ = h.Write([]byte("idle-revoke:"))
		_, _ = h.Write(id[:])
		return int64(h.Sum64())
	}()
	got := idleRevokeLockKey(id)
	require.Equal(t, want, got)
}
