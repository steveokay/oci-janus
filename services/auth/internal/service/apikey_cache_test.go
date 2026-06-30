// Package service — tests for the API-key validation cache (Phase 6.7).
//
// These tests cover the contract the cache must uphold:
//
//   - MISS → Argon2 runs once → cache entry written.
//   - HIT → Argon2 SKIPPED → same identity returned.
//   - HIT + row disabled in the meantime → reject (row-state backstop).
//   - HIT + row expired in the meantime → ErrKeyExpired (row-state backstop).
//   - Wrong secret → NO cache write (no negative caching).
//   - Explicit invalidation on DeleteAPIKey wipes the entry.
//   - Cache key embeds sha256(secret) — a different secret cannot HIT.
//
// All tests share the standard miniredis + fake-repo fixture. The "did
// Argon2 run?" question is answered by a trick: replace the row's stored
// KeyHash with a sentinel value AFTER a successful cold-path validation
// (which has populated the cache) — a HIT will pass because Argon2 is
// skipped, a MISS would fail because the new hash does not match the
// original secret.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyCache_HitBypassesArgon2 verifies the core happy path:
// a second ValidateAPIKey call with the same (id, secret) hits the cache
// and does NOT pay the Argon2 verify cost. We prove "Argon2 was skipped" by
// poisoning the stored KeyHash AFTER the first call — the cold path would
// then reject the second call, but a cache HIT bypasses the verify and
// succeeds.
func TestAPIKeyCache_HitBypassesArgon2(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err, "CreateAPIKey must succeed")

	// First call — cold path. Argon2 runs, identity is cached.
	v1, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err, "first ValidateAPIKey (cold path) must succeed")
	require.Equal(t, userID, v1.UserID, "cold-path UserID must match the owner")

	// Poison the stored KeyHash so a cold-path Argon2.Verify would fail.
	// Reach directly into the fakeAPIKeyRepo via package-internal access.
	require.NotEmpty(t, key.KeyHash, "fixture must have a real hash")
	// Look up the row, replace KeyHash with a syntactically valid but
	// non-matching argon2 string. Real argon2id encoded hashes start with
	// "$argon2id$"; using a known-good encoding of a *different* secret
	// guarantees Verify returns ok=false (not an error).
	row, ok := svc.apiKeys.(*fakeAPIKeyRepo).keys[key.ID]
	require.True(t, ok, "fake repo must hold the key")
	row.KeyHash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAA$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"

	// Second call — must HIT the cache, skip Argon2, and succeed even
	// though the stored hash now refuses the secret.
	v2, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err, "second ValidateAPIKey (HIT) must succeed — proves Argon2 was skipped")
	require.Equal(t, userID, v2.UserID, "HIT-path UserID must match the owner")
}

// TestAPIKeyCache_MissAfterInvalidation verifies that DeleteAPIKey wipes
// the cached identity so a subsequent call with the same (id, secret) no
// longer HITs. (Delete also removes the row, so the actual return is
// ErrInvalidCredentials — but the important assertion is that the cache
// did NOT serve a stale identity.)
func TestAPIKeyCache_MissAfterInvalidation(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err)

	// Populate the cache.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err)

	// Confirm the cache entry exists by reading Redis directly.
	cacheKey := apiKeyCacheKey(key.ID, rawSecret)
	val, getErr := svc.redis.Get(ctx, cacheKey).Result()
	require.NoError(t, getErr, "cache entry must be present after first validation")
	require.NotEmpty(t, val, "cache entry must carry a payload")

	// Delete the key. This both removes the row AND invalidates the cache.
	require.NoError(t, svc.DeleteAPIKey(ctx, key.ID, userID),
		"DeleteAPIKey must succeed for the owner")

	// Cache key must be gone.
	_, getErr = svc.redis.Get(ctx, cacheKey).Result()
	require.Error(t, getErr, "cache entry must be evicted by DeleteAPIKey")
}

// TestAPIKeyCache_HitRespectsExpiry verifies the row-state backstop on the
// HIT path: even if the cache entry is fresh, an expired key row must
// surface ErrKeyExpired (the cache cannot resurrect an expired key).
func TestAPIKeyCache_HitRespectsExpiry(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	// Create with an expiry far in the future so the first call succeeds.
	future := time.Now().Add(1 * time.Hour)
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, &future)
	require.NoError(t, err)

	// Populate the cache.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err, "first call must populate the cache")

	// Mutate the row to have an expiry in the past. The cache entry is
	// untouched (we deliberately bypass the invalidation hook to simulate
	// a clock-based expiry that the application did not write to).
	row := svc.apiKeys.(*fakeAPIKeyRepo).keys[key.ID]
	past := time.Now().Add(-1 * time.Hour)
	row.ExpiresAt = &past

	// HIT path must observe the row's new ExpiresAt and reject.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.ErrorIs(t, err, ErrKeyExpired,
		"HIT path must check ExpiresAt as backstop, returning ErrKeyExpired")
}

// TestAPIKeyCache_HitRespectsIsActive verifies the row-state backstop on
// the HIT path for the is_active flag. If the row is flipped inactive
// out-of-band (the explicit invalidation path missed), the cache must
// NOT serve the stale identity.
func TestAPIKeyCache_HitRespectsIsActive(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err)

	// Populate the cache.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err)

	// Flip the row inactive without going through DeleteAPIKey (so the
	// cache invalidation hook is intentionally skipped). This simulates
	// the worst case: an out-of-band DB update where the cache could
	// otherwise outlive the revocation by up to TTL.
	row := svc.apiKeys.(*fakeAPIKeyRepo).keys[key.ID]
	row.IsActive = false

	// HIT path must observe the row's new IsActive and reject.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.ErrorIs(t, err, ErrInvalidCredentials,
		"HIT path must check IsActive as backstop, returning ErrInvalidCredentials")
}

// TestAPIKeyCache_WrongSecretNotCached verifies the no-negative-cache
// invariant: a failed Argon2 verify MUST NOT write a cache entry, so a
// brute-force attacker must pay the full ~100ms cost on every attempt.
func TestAPIKeyCache_WrongSecretNotCached(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, _, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err)

	// Attempt to validate with a wrong secret. This must FAIL.
	const wrongSecret = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: wrongSecret})
	require.ErrorIs(t, err, ErrInvalidCredentials,
		"wrong secret must be rejected")

	// No cache entry must exist for (keyID, wrongSecret).
	_, getErr := svc.redis.Get(ctx, apiKeyCacheKey(key.ID, wrongSecret)).Result()
	require.Error(t, getErr, "wrong secret must NOT populate the cache (no negative caching)")
}

// TestAPIKeyCache_DifferentSecretCannotHit verifies the security invariant
// that the cache key embeds sha256(secret): two different secrets — even
// for the same key_id — must produce different cache keys, so a stolen
// key_id alone cannot surface a HIT.
func TestAPIKeyCache_DifferentSecretCannotHit(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err)

	// Populate the cache with the real secret.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err)

	// Now build a different secret string and confirm the derived cache
	// key differs (sha256 is collision-resistant for our purposes).
	const differentSecret = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	realKey := apiKeyCacheKey(key.ID, rawSecret)
	otherKey := apiKeyCacheKey(key.ID, differentSecret)
	require.NotEqual(t, realKey, otherKey,
		"cache key must depend on sha256(secret), not just key_id")

	// And confirm the different-secret cache key has no entry — only the
	// real-secret cache key does.
	_, err = svc.redis.Get(ctx, otherKey).Result()
	require.Error(t, err, "different secret must not have a cache entry")
}

// TestAPIKeyCache_KeyContainsSha256OfSecret pins the cache-key derivation
// rule so a future refactor cannot accidentally drop the secret-hash
// component (which would let a stolen key_id mass-hit the cache).
func TestAPIKeyCache_KeyContainsSha256OfSecret(t *testing.T) {
	keyID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	const secret = "deadbeefcafefacefeedfacecafebabebadc0ffee0ddf00ddeadbeefcafefeed"

	got := apiKeyCacheKey(keyID, secret)

	// Recompute the expected suffix independently — if the key format
	// ever changes, this test forces the author to update it deliberately.
	sum := sha256.Sum256([]byte(secret))
	wantSuffix := hex.EncodeToString(sum[:])

	require.Contains(t, got, keyID.String(), "cache key must embed the key_id")
	require.Contains(t, got, wantSuffix, "cache key must embed sha256(secret) as hex")
}

// TestAPIKeyCache_PutAndGetRoundTrip exercises the cache helpers in
// isolation: putCachedValidatedKey writes a payload that
// getCachedValidatedKey decodes losslessly.
func TestAPIKeyCache_PutAndGetRoundTrip(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	keyID := uuid.New()
	secret := "round-trip-test-secret"
	uid := uuid.New()
	tid := uuid.New()
	saID := uuid.New()
	original := &ValidatedKey{
		UserID:           uid,
		TenantID:         tid,
		Access:           nil,
		PrincipalKind:    "service_account",
		ServiceAccountID: &saID,
		EffectiveScopes:  []string{"pull", "push"},
	}

	svc.putCachedValidatedKey(ctx, keyID, secret, original)

	got, ok := svc.getCachedValidatedKey(ctx, keyID, secret)
	require.True(t, ok, "freshly written cache entry must HIT")
	require.Equal(t, uid.String(), got.UserID, "UserID must round-trip")
	require.Equal(t, tid.String(), got.TenantID, "TenantID must round-trip")
	require.Equal(t, "service_account", got.PrincipalKind, "PrincipalKind must round-trip")
	require.Equal(t, saID.String(), got.ServiceAccountID, "ServiceAccountID must round-trip")
	require.Equal(t, []string{"pull", "push"}, got.EffectiveScopes, "EffectiveScopes must round-trip")
}

// TestAPIKeyCache_InvalidateAfterPut verifies InvalidateAPIKeyCache drops
// both the cache entry and the per-key index set so a future write starts
// from clean state.
func TestAPIKeyCache_InvalidateAfterPut(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	ctx := context.Background()
	keyID := uuid.New()
	secret := "invalidation-test-secret"
	vk := &ValidatedKey{
		UserID:          uuid.New(),
		TenantID:        uuid.New(),
		PrincipalKind:   "human",
		EffectiveScopes: []string{"pull"},
	}

	svc.putCachedValidatedKey(ctx, keyID, secret, vk)

	// Sanity-check: the entry exists.
	_, ok := svc.getCachedValidatedKey(ctx, keyID, secret)
	require.True(t, ok, "cache entry must be present after put")

	// Invalidate.
	svc.InvalidateAPIKeyCache(ctx, keyID)

	// Now it must be gone.
	_, ok = svc.getCachedValidatedKey(ctx, keyID, secret)
	require.False(t, ok, "cache entry must be evicted by InvalidateAPIKeyCache")

	// And the per-key index set must be gone too.
	members, _ := svc.redis.SMembers(ctx, apiKeyCacheIndexKey(keyID)).Result()
	require.Empty(t, members, "index set must be wiped by InvalidateAPIKeyCache")
}

// TestAPIKeyCache_RedisDownFailsOpen verifies that a Redis error on Get
// behaves like a MISS: the caller falls through to the cold path rather
// than failing the request. We use the redis-error-injection wrapper to
// simulate Redis being unreachable for cache lookups.
func TestAPIKeyCache_RedisDownFailsOpen(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "ci-bot", nil, nil)
	require.NoError(t, err)

	// Swap in a wrapper that returns an error on apikey:valid: Get calls.
	// The wrapper still delegates everything else (writes, JTI checks, the
	// principal-revocation check) to the real miniredis-backed client so
	// other Service paths keep working.
	realClient, ok := svc.redis.(*redis.Client)
	require.True(t, ok, "test fixture must use *redis.Client (miniredis-backed)")
	svc.redis = &apikeyCacheErrRedis{Client: realClient}

	// Validate — must still succeed via the cold path even though the
	// cache Get errors out.
	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	require.NoError(t, err,
		"a Redis error on the cache Get must fail-open (MISS → cold path), not break auth")
}

// apikeyCacheErrRedis is a test-only redisClient wrapper that injects a
// synthetic error for "apikey:valid:*" Get calls so the fail-open path
// in getCachedValidatedKey is exercisable in unit tests. It embeds
// *redis.Client so every method it does NOT override is delegated
// transparently.
type apikeyCacheErrRedis struct {
	*redis.Client
}

// Get returns an injected error for apikey:valid:* lookups and delegates
// for every other key — including the apikey:cached:* index Get calls
// (which the cache HIT path does not use; only the invalidation path
// uses them via SMembers).
func (r *apikeyCacheErrRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	const prefix = "apikey:valid:"
	if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
		return redis.NewStringResult("", errors.New("injected Redis unavailable"))
	}
	return r.Client.Get(ctx, key)
}
