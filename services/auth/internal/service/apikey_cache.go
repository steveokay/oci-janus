// Package service — API-key validation cache (REDESIGN-001 Phase 6.7).
//
// Argon2id verification is intentionally expensive (~50-100ms on commodity
// hardware) — the right cost for a one-shot human login, but a wasted CPU
// burn when a CI bot re-presents the same key 100 req/s. This file adds a
// short-TTL Redis cache around the verify step so that, on the steady-state
// hot path, the cost collapses to a sub-millisecond Redis GET plus the same
// DB row-state checks the cold path runs.
//
// Security invariants preserved:
//   - Cache key embeds sha256(secret): a stolen key_id alone cannot surface a
//     cached identity. The cache is "we already verified this specific
//     secret"; it is NOT a "this key_id has been seen" cache.
//   - Cache MISS always falls through to full Argon2 verify — never a fast
//     fail. The cache is an optimisation, not a security boundary.
//   - Cache stores POSITIVE results only — a wrong secret must always pay
//     the Argon2 cost so brute-force attackers cannot mass-test cheaply.
//   - Row state (is_active, expires_at, SA disabled, scope intersection) is
//     re-checked from the DB on every HIT, so a stale cache cannot outlive a
//     revocation by more than the TTL (60s).
//   - Redis unreachable → log warn, treat as MISS. The cache must never
//     break authentication.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// apiKeyCacheTTL is the lifetime of a positive ValidateAPIKey result in Redis.
// Short enough that a revoked/disabled key cannot stay live for longer than
// this window even if the explicit invalidation path misses (e.g. Redis was
// momentarily unreachable when the DB write fired). Long enough to soak the
// bulk of CI-bot RPS: at 60s with one request/s steady state, the Argon2
// verify runs ~1× per minute per key instead of every push.
const apiKeyCacheTTL = 60 * time.Second

// cachedValidatedKey is the wire form of a ValidatedKey written to Redis.
// We serialise the full identity so the HIT path can reconstruct exactly
// what the cold path would have returned (UserID for shadow-user JWT
// issuance, ServiceAccountID for downstream SA gates, EffectiveScopes for
// the scope intersection that the SA branch already paid for).
//
// JSON is deliberate: it survives schema additions (new optional fields
// decode as zero values on existing cache entries) and is human-readable
// for ops debugging via `redis-cli GET`.
type cachedValidatedKey struct {
	UserID           string   `json:"user_id"`
	TenantID         string   `json:"tenant_id"`
	PrincipalKind    string   `json:"principal_kind"`
	ServiceAccountID string   `json:"sa_id,omitempty"`
	EffectiveScopes  []string `json:"scopes,omitempty"`
}

// apiKeyCacheKey returns the Redis key under which a positive ValidateAPIKey
// result is cached. The sha256 of the secret is part of the key so a stolen
// key_id (e.g. recovered from a log file) cannot produce a HIT: the attacker
// would still need the secret to derive the same hash.
//
// We use sha256 here only as a fingerprint — secret authenticity is still
// enforced by Argon2 on the MISS path. Embedding raw secret material in a
// Redis key would be a footgun; the sha256 lets us look up "this exact
// (id, secret) pair" without ever putting the secret itself near Redis.
func apiKeyCacheKey(keyID uuid.UUID, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return "apikey:valid:" + keyID.String() + ":" + hex.EncodeToString(sum[:])
}

// apiKeyCacheIndexKey returns the Redis key that lists every (sha256-of-
// secret) value currently cached for a given key_id. Used by the explicit
// invalidation path (DeleteAPIKey, DisableAPIKeysForUser, SA SetDisabled)
// to DEL every cached identity for a key without having to SCAN.
//
// In practice each api_keys row has exactly one valid secret, so the index
// set holds at most one element — the index exists for correctness, not
// because we expect bulk fan-out.
func apiKeyCacheIndexKey(keyID uuid.UUID) string {
	return "apikey:cached:" + keyID.String()
}

// getCachedValidatedKey looks up a previously verified (key_id, secret) pair
// in Redis. Returns (nil, false) for a clean MISS (so the caller falls through
// to Argon2). Returns (nil, false) plus a warn log on any Redis error other
// than redis.Nil — Redis failures must not break auth.
func (s *Service) getCachedValidatedKey(ctx context.Context, keyID uuid.UUID, secret string) (*cachedValidatedKey, bool) {
	if s.redis == nil {
		return nil, false
	}
	raw, err := s.redis.Get(ctx, apiKeyCacheKey(keyID, secret)).Result()
	if err != nil {
		// redis.Nil is the normal MISS case — silent fall-through.
		if !errors.Is(err, redis.Nil) {
			// Real error (network, auth, etc.) — log once and behave as MISS.
			// Do NOT propagate: the cache is an optimisation; the cold path
			// is the authoritative path and is fully functional without Redis.
			slog.WarnContext(ctx, "apikey cache: get failed; falling through to Argon2",
				"key_id", keyID, "err", err)
		}
		return nil, false
	}
	var c cachedValidatedKey
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		// A malformed cache entry is treated as a MISS rather than a hard
		// error. This recovers gracefully from a schema change that ships
		// without a key prefix bump.
		slog.WarnContext(ctx, "apikey cache: malformed entry; falling through to Argon2",
			"key_id", keyID, "err", err)
		return nil, false
	}
	return &c, true
}

// putCachedValidatedKey writes a positive ValidateAPIKey result to Redis
// with TTL = apiKeyCacheTTL. Also pushes the secret-hash into the per-keyID
// index set so the explicit invalidation path can find it without SCAN.
//
// Errors are logged and swallowed: a cache write failure must never fail
// the authentication request the caller is currently completing. The worst
// case is the next call re-pays the Argon2 cost.
func (s *Service) putCachedValidatedKey(ctx context.Context, keyID uuid.UUID, secret string, v *ValidatedKey) {
	if s.redis == nil {
		return
	}
	// Serialise the identity. We marshal only the fields downstream needs —
	// dropping Access (which is derivable from EffectiveScopes via
	// mapScopesToAccess) keeps the cache entry compact.
	saID := ""
	if v.ServiceAccountID != nil {
		saID = v.ServiceAccountID.String()
	}
	payload := cachedValidatedKey{
		UserID:           v.UserID.String(),
		TenantID:         v.TenantID.String(),
		PrincipalKind:    v.PrincipalKind,
		ServiceAccountID: saID,
		EffectiveScopes:  v.EffectiveScopes,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal cannot fail for this concrete type — but if some
		// future field carries a non-marshalable value (NaN, channel, etc.)
		// we swallow rather than blow up the auth path.
		slog.WarnContext(ctx, "apikey cache: marshal failed; skipping write",
			"key_id", keyID, "err", err)
		return
	}
	// Compute the cache key + the secret-hash fragment used in the index.
	cacheKey := apiKeyCacheKey(keyID, secret)
	sum := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(sum[:])

	// Pipeline both writes so they hit Redis in one round-trip. We do not
	// require atomicity (a partial write only means a slightly weaker
	// invalidation contract — the row-state HIT check still backs us up).
	pipe := s.redis.Pipeline()
	pipe.Set(ctx, cacheKey, encoded, apiKeyCacheTTL)
	pipe.SAdd(ctx, apiKeyCacheIndexKey(keyID), hash)
	// Refresh the index TTL on every write so the set expires shortly after
	// its last contained entry — keeps Redis tidy without a janitor.
	pipe.Expire(ctx, apiKeyCacheIndexKey(keyID), apiKeyCacheTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.WarnContext(ctx, "apikey cache: write failed",
			"key_id", keyID, "err", err)
	}
}

// InvalidateAPIKeyCache deletes every cached ValidateAPIKey result for the
// given key_id. Called from the API-key disable/delete paths so a revoked
// key cannot keep producing cache HITs for the remainder of the TTL.
//
// Exported because services/auth's server.go wires this as the
// APIKeyCacheInvalidator hook on ServiceAccountService (see
// SetAPIKeyCacheInvalidator), letting SA lifecycle mutations proactively
// wipe cached identities for their keys.
//
// The HIT path also re-checks row state from the DB as a backstop, so a
// failure here is best-effort: log and continue. The next request that
// validates this key will reload the row, observe is_active=false, and
// reject — within at most apiKeyCacheTTL.
//
// Pass any number of key IDs; an empty slice is a no-op.
func (s *Service) InvalidateAPIKeyCache(ctx context.Context, keyIDs ...uuid.UUID) {
	if s.redis == nil || len(keyIDs) == 0 {
		return
	}
	for _, keyID := range keyIDs {
		idxKey := apiKeyCacheIndexKey(keyID)
		// Pull the list of secret-hashes currently cached for this key.
		// Each member is the hex sha256 of a secret we have validated;
		// re-derive the full cache key by concatenation.
		hashes, err := s.redis.SMembers(ctx, idxKey).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			slog.WarnContext(ctx, "apikey cache: invalidation SMembers failed",
				"key_id", keyID, "err", err)
			// Keep going: try to at least DEL the index set so a future
			// write rebuilds from clean state.
			_ = s.redis.Del(ctx, idxKey).Err()
			continue
		}
		// Build the full key set to delete: one entry per known hash plus
		// the index itself. Skip the index-only DEL when there are no
		// hashes — Redis handles a single-key Del fine.
		toDel := make([]string, 0, len(hashes)+1)
		for _, h := range hashes {
			toDel = append(toDel, "apikey:valid:"+keyID.String()+":"+h)
		}
		toDel = append(toDel, idxKey)
		if err := s.redis.Del(ctx, toDel...).Err(); err != nil {
			slog.WarnContext(ctx, "apikey cache: invalidation Del failed",
				"key_id", keyID, "err", err)
		}
	}
}
