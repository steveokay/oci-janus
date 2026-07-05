//go:build integration

// Package repository — apikey_review_test.go covers the FUT-004 access-
// review projection: ListStaleKeys returns the union of "idle" and
// "rotation-lapsed" keys (excluding snoozed rows), SetReviewSnoozedUntil
// round-trips a snooze timestamp, and GetTenantIDForKey plays the
// principal-identity role for the BFF's owner-vs-admin gate.
//
// Tests build against a real PostgreSQL 16 container (testcontainers).
// The suite reuses gooseUpTo + containers.Postgres helpers already in the
// integration-tagged test package.
//
// Sub-tests:
//   - ListStaleKeys_IdleKeyReturned
//   - ListStaleKeys_RotationLapsedReturned
//   - ListStaleKeys_NeverUsedButOldReturned
//   - ListStaleKeys_NeverUsedButRecentExcluded
//   - ListStaleKeys_SnoozedKeyExcluded
//   - ListStaleKeys_RecentKeyExcluded
//   - ListStaleKeys_RevokedKeyExcluded
//   - SetReviewSnoozedUntil_UpdatesAndClears
//   - SetReviewSnoozedUntil_NotFoundOnMissing
//   - GetTenantIDForKey_ReturnsRow
//   - GetTenantIDForKey_ReturnsNotFoundOnMissing
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// reviewTestFixture spins up a Postgres container migrated to the FUT-004
// migration (20260703000001) and returns a pool + api-key repo + a fresh
// tenant id + a "seed a human user" helper. Each caller gets its own
// tenant so sub-tests don't step on each other's rows.
type reviewTestFixture struct {
	pool    *pgxpool.Pool
	apiKeys *APIKeyRepository
	tenant  uuid.UUID
}

// setupReviewFixture is the one-line container + migration boot used by
// every access-review sub-test.
func setupReviewFixture(t *testing.T, ctx context.Context) *reviewTestFixture {
	t.Helper()
	dsn := containers.Postgres(t)
	gooseUpTo(t, dsn, "20260703000001")
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return &reviewTestFixture{
		pool:    pool,
		apiKeys: NewAPIKeyRepository(pool),
		tenant:  uuid.New(),
	}
}

// seedHumanUser inserts a fresh human user in the fixture's tenant and
// returns its id. Uses UUID randomness in the username so the same
// fixture can seed many users without unique-constraint collisions.
func (f *reviewTestFixture) seedHumanUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	var userID uuid.UUID
	suffix := uuid.New().String()[:8]
	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, 'u-'||$2, $2||'@example.invalid', '', 'human')
		RETURNING id`, f.tenant, suffix).Scan(&userID))
	return userID
}

// seedKey inserts an api_keys row owned by userID with the provided
// (last_used_at, rotation_due_at, review_snoozed_until, is_active). Nil
// pointers persist as NULL. Returns the new key id.
func (f *reviewTestFixture) seedKey(
	t *testing.T,
	ctx context.Context,
	userID uuid.UUID,
	name string,
	lastUsed *time.Time,
	rotationDue *time.Time,
	snoozed *time.Time,
	isActive bool,
) uuid.UUID {
	t.Helper()
	var keyID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `
		INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes,
		                     last_used_at, rotation_due_at, review_snoozed_until, is_active)
		VALUES ($1, $2, $3, 'hash', 'prefix'||substr(md5(random()::text),1,6), '{}',
		        $4, $5, $6, $7)
		RETURNING id`, f.tenant, userID, name, lastUsed, rotationDue, snoozed, isActive).Scan(&keyID))
	return keyID
}

// TestAPIKeyRepo_ListStaleKeys covers all "included / excluded" sub-cases
// in one container (fresh tenant per sub-test keeps them isolated).
func TestAPIKeyRepo_ListStaleKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("IdleKeyReturned", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		oldLastUsed := now.Add(-100 * 24 * time.Hour)
		keyID := f.seedKey(t, ctx, u, "idle", &oldLastUsed, nil, nil, true)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, keyID, got[0].ID)
		require.Equal(t, u, got[0].OwnerUserID)
	})

	t.Run("RotationLapsedReturned", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		fresh := now.Add(-1 * time.Hour)
		rotation := now.Add(-1 * time.Hour) // rotation deadline in the past
		keyID := f.seedKey(t, ctx, u, "rotlapsed", &fresh, &rotation, nil, true)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, keyID, got[0].ID)
	})

	t.Run("NeverUsedButOldReturned", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		// last_used_at NULL — a never-used key is stale only once it has sat
		// unused past the cutoff SINCE CREATION, so age created_at back.
		keyID := f.seedKey(t, ctx, u, "neverused-old", nil, nil, nil, true)
		_, err := f.pool.Exec(ctx,
			`UPDATE api_keys SET created_at = now() - interval '100 days' WHERE id = $1`, keyID)
		require.NoError(t, err)

		cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, keyID, got[0].ID)
	})

	t.Run("NeverUsedButRecentExcluded", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		// A brand-new never-used key (created_at defaults to now()) is NOT
		// stale — it just hasn't been put to work yet. This pins the grace
		// period that keeps freshly-issued keys from being flagged/revoked
		// before the operator can use them (the FUT-003 idle-revoke fix).
		_ = f.seedKey(t, ctx, u, "neverused-new", nil, nil, nil, true)

		cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Empty(t, got, "freshly-created never-used key must not be stale")
	})

	t.Run("SnoozedKeyExcluded", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		oldLastUsed := now.Add(-100 * 24 * time.Hour)
		snoozedUntil := now.Add(20 * 24 * time.Hour) // snoozed 20 days into future
		_ = f.seedKey(t, ctx, u, "snoozed", &oldLastUsed, nil, &snoozedUntil, true)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Empty(t, got, "snoozed key must not appear in stale list")
	})

	t.Run("RecentKeyExcluded", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		recent := now.Add(-1 * time.Hour)
		_ = f.seedKey(t, ctx, u, "recent", &recent, nil, nil, true)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Empty(t, got, "recently used key must be excluded")
	})

	t.Run("RevokedKeyExcluded", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		oldLastUsed := now.Add(-100 * 24 * time.Hour)
		// is_active=false → excluded even though otherwise stale.
		_ = f.seedKey(t, ctx, u, "revoked", &oldLastUsed, nil, nil, false)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Empty(t, got, "revoked key must be excluded from stale list")
	})

	t.Run("SnoozeExpired_KeyReturned", func(t *testing.T) {
		// A snooze in the past must NOT exclude the key — it's expired.
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		oldLastUsed := now.Add(-100 * 24 * time.Hour)
		expiredSnooze := now.Add(-1 * time.Hour)
		keyID := f.seedKey(t, ctx, u, "expired-snooze", &oldLastUsed, nil, &expiredSnooze, true)

		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, keyID, got[0].ID)
	})
}

// TestAPIKeyRepo_SetReviewSnoozedUntil round-trips the snooze column
// (update + clear) and asserts the not-found path.
func TestAPIKeyRepo_SetReviewSnoozedUntil(t *testing.T) {
	ctx := context.Background()

	t.Run("UpdatesAndClears", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		now := time.Now().UTC()
		oldLastUsed := now.Add(-100 * 24 * time.Hour)
		keyID := f.seedKey(t, ctx, u, "snoozetest", &oldLastUsed, nil, nil, true)

		// Snooze 30 days into the future.
		until := now.Add(30 * 24 * time.Hour)
		require.NoError(t, f.apiKeys.SetReviewSnoozedUntil(ctx, keyID, &until))

		// After the snooze the key should be excluded from stale list.
		cutoff := now.Add(-30 * 24 * time.Hour)
		got, err := f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Empty(t, got, "key must be excluded once snoozed")

		// Now clear the snooze — the key should reappear.
		require.NoError(t, f.apiKeys.SetReviewSnoozedUntil(ctx, keyID, nil))
		got, err = f.apiKeys.ListStaleKeys(ctx, f.tenant, cutoff)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, keyID, got[0].ID)
	})

	t.Run("NotFoundOnMissing", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		until := time.Now().UTC().Add(24 * time.Hour)
		err := f.apiKeys.SetReviewSnoozedUntil(ctx, uuid.New(), &until)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrNotFound), "want ErrNotFound, got %v", err)
	})
}

// TestAPIKeyRepo_GetTenantIDForKey covers the BFF's principal-identity
// lookup path used to enforce "owner OR admin" on Snooze.
func TestAPIKeyRepo_GetTenantIDForKey(t *testing.T) {
	ctx := context.Background()

	t.Run("ReturnsRow", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		u := f.seedHumanUser(t, ctx)
		keyID := f.seedKey(t, ctx, u, "lookup", nil, nil, nil, true)

		gotTenant, gotOwner, err := f.apiKeys.GetTenantIDForKey(ctx, keyID)
		require.NoError(t, err)
		require.Equal(t, f.tenant, gotTenant)
		require.Equal(t, u, gotOwner)
	})

	t.Run("ReturnsNotFoundOnMissing", func(t *testing.T) {
		f := setupReviewFixture(t, ctx)
		_, _, err := f.apiKeys.GetTenantIDForKey(ctx, uuid.New())
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrNotFound), "want ErrNotFound, got %v", err)
	})
}
