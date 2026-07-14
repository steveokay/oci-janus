//go:build integration

// Package repository — token_policy_test.go exercises the migration
// round-trip + CRUD repository for the FUT-003 token_policies table
// and the api_keys extensions (UpdateLastUsedAt, SetRotationDueAt,
// RevokeWithReason, ListIdleKeys) against a real PostgreSQL 16 container.
//
// Covers:
//   - GetOrDefault returns empty policy for unset tenant (no error).
//   - Upsert inserts a new row.
//   - Upsert updates an existing row.
//   - Upsert preserves un-set fields via COALESCE (partial update).
//   - api_keys.UpdateLastUsedAt / SetRotationDueAt round-trip.
//   - api_keys.ListIdleKeys respects the cutoff + excludes revoked keys.
//   - api_keys.RevokeWithReason flips is_active + records the reason.
//
// The sub-tests share a container + pool so total runtime stays under
// ~10s on a developer laptop.
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// TestTokenPolicyRepo covers every public method of TokenPolicyRepo.
func TestTokenPolicyRepo(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgxpool.New")
	t.Cleanup(pool.Close)

	// Migrate up to and including the TOTP-MFA migration (20260705120000)
	// which adds token_policies.require_mfa — needed by the require_mfa
	// round-trip sub-test below.
	gooseUpTo(t, dsn, "20260705120000")

	repo := NewTokenPolicyRepo(pool)

	t.Run("GetOrDefault_ReturnsEmptyForUnsetTenant", func(t *testing.T) {
		tenantID := uuid.New()
		got, err := repo.GetOrDefault(ctx, tenantID)
		require.NoError(t, err)
		require.Equal(t, tenantID, got.TenantID)
		require.Nil(t, got.MaxTTLDays, "unset tenant should have nil MaxTTLDays")
		require.Nil(t, got.RotationIntervalDays)
		require.Nil(t, got.IdleRevokeDays)
	})

	t.Run("Upsert_InsertsNewRow", func(t *testing.T) {
		tenantID := uuid.New()
		actor := uuid.New()
		ttl := int32(30)
		got, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			MaxTTLDays:      &ttl,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)
		require.Equal(t, tenantID, got.TenantID)
		require.NotNil(t, got.MaxTTLDays)
		require.Equal(t, int32(30), *got.MaxTTLDays)
		require.Nil(t, got.RotationIntervalDays, "un-set field stays nil")
		require.Nil(t, got.IdleRevokeDays, "un-set field stays nil")
		require.NotNil(t, got.UpdatedByUserID)
		require.Equal(t, actor, *got.UpdatedByUserID)
	})

	t.Run("Upsert_UpdatesExistingRow", func(t *testing.T) {
		tenantID := uuid.New()
		actor1 := uuid.New()
		ttl1 := int32(30)
		_, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			MaxTTLDays:      &ttl1,
			UpdatedByUserID: &actor1,
		})
		require.NoError(t, err)

		// Second Upsert lifts the cap AND rotates the actor.
		actor2 := uuid.New()
		ttl2 := int32(60)
		got, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			MaxTTLDays:      &ttl2,
			UpdatedByUserID: &actor2,
		})
		require.NoError(t, err)
		require.NotNil(t, got.MaxTTLDays)
		require.Equal(t, int32(60), *got.MaxTTLDays)
		require.NotNil(t, got.UpdatedByUserID)
		require.Equal(t, actor2, *got.UpdatedByUserID)
	})

	t.Run("Upsert_PreservesUnsetFieldsOnPartialUpdate", func(t *testing.T) {
		tenantID := uuid.New()
		actor := uuid.New()
		ttl := int32(30)
		rot := int32(90)
		idle := int32(45)
		// Seed all three.
		_, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:             tenantID,
			MaxTTLDays:           &ttl,
			RotationIntervalDays: &rot,
			IdleRevokeDays:       &idle,
			UpdatedByUserID:      &actor,
		})
		require.NoError(t, err)

		// Partial update: bump only MaxTTLDays. Others must stay put.
		ttl2 := int32(120)
		got, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			MaxTTLDays:      &ttl2,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)
		require.NotNil(t, got.MaxTTLDays)
		require.Equal(t, int32(120), *got.MaxTTLDays, "MaxTTLDays should have been updated")
		require.NotNil(t, got.RotationIntervalDays, "RotationIntervalDays should have been preserved")
		require.Equal(t, int32(90), *got.RotationIntervalDays)
		require.NotNil(t, got.IdleRevokeDays, "IdleRevokeDays should have been preserved")
		require.Equal(t, int32(45), *got.IdleRevokeDays)
	})

	t.Run("RequireMFA_RoundTripsAndDefaultsFalse", func(t *testing.T) {
		// Default: an unset tenant reads require_mfa=false.
		unset := uuid.New()
		got, err := repo.GetOrDefault(ctx, unset)
		require.NoError(t, err)
		require.False(t, got.RequireMFA, "unset tenant defaults to require_mfa=false")

		// Upsert require_mfa=true, then read it back.
		tenantID := uuid.New()
		actor := uuid.New()
		up, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			RequireMFA:      true,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)
		require.True(t, up.RequireMFA, "Upsert should persist require_mfa=true")

		read, err := repo.GetOrDefault(ctx, tenantID)
		require.NoError(t, err)
		require.True(t, read.RequireMFA, "GetOrDefault should return persisted require_mfa=true")

		// Flipping back to false is an unconditional write (no preserve).
		down, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			RequireMFA:      false,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)
		require.False(t, down.RequireMFA, "Upsert should overwrite require_mfa back to false")
	})

	t.Run("ListTenantsWithIdleRevoke_ReturnsOnlyConfigured", func(t *testing.T) {
		// Tenant A has idle_revoke_days set; tenant B does not.
		tenantA := uuid.New()
		tenantB := uuid.New()
		idle := int32(30)
		actor := uuid.New()

		_, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantA,
			IdleRevokeDays:  &idle,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)

		ttl := int32(30)
		_, err = repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantB,
			MaxTTLDays:      &ttl, // no idle_revoke_days
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)

		ids, err := repo.ListTenantsWithIdleRevoke(ctx)
		require.NoError(t, err)
		require.Contains(t, ids, tenantA)
		require.NotContains(t, ids, tenantB)
	})

	t.Run("Clear_RemovesRow", func(t *testing.T) {
		tenantID := uuid.New()
		actor := uuid.New()
		ttl := int32(30)
		_, err := repo.Upsert(ctx, TokenPolicy{
			TenantID:        tenantID,
			MaxTTLDays:      &ttl,
			UpdatedByUserID: &actor,
		})
		require.NoError(t, err)

		require.NoError(t, repo.Clear(ctx, tenantID))

		got, err := repo.GetOrDefault(ctx, tenantID)
		require.NoError(t, err)
		require.Nil(t, got.MaxTTLDays, "after Clear, GetOrDefault should return zero policy")
	})
}

// TestAPIKeyRepoFUT003 exercises the FUT-003 additions to APIKeyRepository:
// UpdateLastUsedAt, SetRotationDueAt, RevokeWithReason, ListIdleKeys.
func TestAPIKeyRepoFUT003(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgxpool.New")
	t.Cleanup(pool.Close)

	gooseUpTo(t, dsn, "20260702000001")

	repo := NewAPIKeyRepository(pool)

	tenantID := uuid.New()

	// Helper: create a human user + one API key belonging to it, so the
	// FK to users(id) is satisfied for every seeded api_keys row.
	mkKey := func(name string, lastUsed *time.Time) uuid.UUID {
		t.Helper()
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

	t.Run("UpdateLastUsedAt_UpdatesTimestamp", func(t *testing.T) {
		keyID := mkKey("update-last-used", nil)
		now := time.Now().UTC().Truncate(time.Millisecond)
		require.NoError(t, repo.UpdateLastUsedAt(ctx, keyID, now))

		var got *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT last_used_at FROM api_keys WHERE id = $1`, keyID).Scan(&got))
		require.NotNil(t, got)
		require.WithinDuration(t, now, *got, time.Second)
	})

	t.Run("SetRotationDueAt_UpdatesTimestamp", func(t *testing.T) {
		keyID := mkKey("set-rotation", nil)
		due := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Millisecond)
		require.NoError(t, repo.SetRotationDueAt(ctx, keyID, &due))

		var got *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT rotation_due_at FROM api_keys WHERE id = $1`, keyID).Scan(&got))
		require.NotNil(t, got)
		require.WithinDuration(t, due, *got, time.Second)

		// Clearing.
		require.NoError(t, repo.SetRotationDueAt(ctx, keyID, nil))
		require.NoError(t, pool.QueryRow(ctx, `SELECT rotation_due_at FROM api_keys WHERE id = $1`, keyID).Scan(&got))
		require.Nil(t, got, "SetRotationDueAt(nil) should clear the column")
	})

	t.Run("RevokeWithReason_FlipsActiveAndRecordsReason", func(t *testing.T) {
		keyID := mkKey("revoke-me", nil)
		require.NoError(t, repo.RevokeWithReason(ctx, keyID, "idle_revoked"))

		var active bool
		var reason *string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT is_active, revoke_reason FROM api_keys WHERE id = $1`, keyID,
		).Scan(&active, &reason))
		require.False(t, active)
		require.NotNil(t, reason)
		require.Equal(t, "idle_revoked", *reason)

		// Second revoke is a no-op — the row is already inactive.
		err := repo.RevokeWithReason(ctx, keyID, "manual")
		require.ErrorIs(t, err, ErrNotFound, "revoking an already-revoked key should be ErrNotFound (defensive)")
	})

	t.Run("ListIdleKeys_ReturnsOnlyKeysOlderThanThreshold", func(t *testing.T) {
		// Idleness is anchored to COALESCE(last_used_at, created_at) — a
		// never-used key falls back to its creation time and earns the SAME
		// idle grace period as a used key (fix #276). So the "never used"
		// dimension only decides idleness *together with* created_at:
		//   - idleKey       — last_used_at well before cutoff              (return)
		//   - freshKey      — last_used_at well after cutoff               (skip)
		//   - oldNeverUsed  — NULL last_used_at, created_at before cutoff  (return)
		//   - newNeverUsed  — NULL last_used_at, created_at after cutoff   (skip)
		// The last row is the regression #276 fixed: a freshly-issued key
		// must NOT be revoked on the next tick before it is ever wired up.
		idleTenant := uuid.New()

		// mkKeyInTenant seeds a key with an explicit last_used_at (may be nil)
		// and created_at, so the test can drive the COALESCE anchor precisely.
		mkKeyInTenant := func(tenant uuid.UUID, name string, lastUsed *time.Time, createdAt time.Time) uuid.UUID {
			var userID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `
				INSERT INTO users (tenant_id, username, email, password_hash, kind)
				VALUES ($1, 'u-'||$2, $2||'@example.invalid', '', 'human')
				RETURNING id`, tenant, name).Scan(&userID))
			var keyID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `
				INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes, last_used_at, created_at)
				VALUES ($1, $2, $3, 'hash', 'prefix'||substr(md5(random()::text),1,6), '{}', $4, $5)
				RETURNING id`, tenant, userID, name, lastUsed, createdAt).Scan(&keyID))
			return keyID
		}

		now := time.Now().UTC()
		oldTime := now.Add(-30 * 24 * time.Hour)
		recentTime := now.Add(-1 * time.Hour)

		idleKey := mkKeyInTenant(idleTenant, "idle", &oldTime, oldTime)
		freshKey := mkKeyInTenant(idleTenant, "fresh", &recentTime, oldTime)
		// Never-used, created long ago → COALESCE anchor is old → idle.
		oldNeverUsed := mkKeyInTenant(idleTenant, "old-never-used", nil, oldTime)
		// Never-used, created just now → COALESCE anchor is recent → NOT idle.
		newNeverUsed := mkKeyInTenant(idleTenant, "new-never-used", nil, recentTime)

		// Cutoff = 7 days ago — anything last-active before this counts.
		cutoff := now.Add(-7 * 24 * time.Hour)
		got, err := repo.ListIdleKeys(ctx, idleTenant, cutoff)
		require.NoError(t, err)

		ids := make(map[uuid.UUID]bool, len(got))
		for _, k := range got {
			ids[k.ID] = true
		}
		require.True(t, ids[idleKey], "idle key should be listed")
		require.True(t, ids[oldNeverUsed], "never-used key created before cutoff should be listed")
		require.False(t, ids[newNeverUsed], "never-used key created after cutoff should NOT be listed (grace period, #276)")
		require.False(t, ids[freshKey], "fresh key should NOT be listed")
	})

	t.Run("ListIdleKeys_ExcludesRevokedKeys", func(t *testing.T) {
		// Revoked keys must not appear — the partial index filters them out
		// so the worker doesn't re-process a key it already revoked last tick.
		revokedTenant := uuid.New()

		mkKeyInTenant := func(tenant uuid.UUID, name string, lastUsed *time.Time) uuid.UUID {
			var userID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `
				INSERT INTO users (tenant_id, username, email, password_hash, kind)
				VALUES ($1, 'u-'||$2, $2||'@example.invalid', '', 'human')
				RETURNING id`, tenant, name).Scan(&userID))
			var keyID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `
				INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes, last_used_at)
				VALUES ($1, $2, $3, 'hash', 'prefix'||substr(md5(random()::text),1,6), '{}', $4)
				RETURNING id`, tenant, userID, name, lastUsed).Scan(&keyID))
			return keyID
		}

		now := time.Now().UTC()
		old := now.Add(-30 * 24 * time.Hour)

		activeIdle := mkKeyInTenant(revokedTenant, "active-idle", &old)
		revokedIdle := mkKeyInTenant(revokedTenant, "revoked-idle", &old)

		require.NoError(t, repo.RevokeWithReason(ctx, revokedIdle, "manual"))

		cutoff := now.Add(-7 * 24 * time.Hour)
		got, err := repo.ListIdleKeys(ctx, revokedTenant, cutoff)
		require.NoError(t, err)
		ids := make(map[uuid.UUID]bool, len(got))
		for _, k := range got {
			ids[k.ID] = true
		}
		require.True(t, ids[activeIdle], "active idle key should be listed")
		require.False(t, ids[revokedIdle], "revoked key must NOT be listed")
	})
}
