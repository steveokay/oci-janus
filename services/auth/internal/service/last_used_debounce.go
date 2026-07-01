// Package service — last_used_debounce.go debounces api_keys.last_used_at
// updates via Redis so high-RPS CI bots don't turn ValidateAPIKey into an
// unbounded write path.
//
// Design (FUT-003):
//
//   - Every successful ValidateAPIKey fires a fire-and-forget Touch.
//   - The updater does a Redis SET NX EX 300s on "lastused:debounce:<key_id>".
//     If SET NX succeeds (no prior tick within the window) we write the DB row.
//     If SET NX fails (another tick already claimed the window) we skip the write.
//   - If Redis is unreachable or errors, we fail-OPEN and run the DB write
//     inline. The debounce is an optimisation, not a security boundary —
//     the worst case for a Redis-down window is that we write a few extra
//     rows to the DB, which the writable index absorbs comfortably.
//
// The window (5 min) matches the manifests.last_pulled_at posture: tight
// enough for the idle-revoke worker's per-tenant evaluation, loose enough
// that a bot polling every second writes at most one DB row per 5 min.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// lastUsedRedis is the narrow Redis surface the debouncer needs. Satisfied
// by the process-wide redisClient interface and by *redis.Client directly
// so tests that hold either shape can plug in without adapters.
type lastUsedRedis interface {
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
}

// lastUsedDebounceWindow bounds how often we write last_used_at per key.
// Set to 5 minutes so a bot polling every second writes at most one DB
// row per 5-minute window. The idle-revoke worker uses last_used_at with
// day-granularity thresholds so a 5-minute skew doesn't affect its
// correctness.
const lastUsedDebounceWindow = 5 * time.Minute

// apiKeyLastUsedRepo is the narrow interface the debouncer uses. Kept
// small so tests can supply a fake that counts calls without a full pgx
// stack. Satisfied by *repository.APIKeyRepository via UpdateLastUsedAt.
type apiKeyLastUsedRepo interface {
	UpdateLastUsedAt(ctx context.Context, id uuid.UUID, at time.Time) error
}

// lastUsedUpdater debounces api_keys.last_used_at updates via Redis.
// Constructed at Service startup with the process-wide Redis client + the
// APIKeyRepo. Never blocks the auth hot path: Touch spawns a goroutine and
// returns immediately.
//
// The redis field may be nil (dev stacks that boot without a broker); in
// that case every Touch runs the DB write inline. Losing the debounce is
// a performance regression, not a security one.
type lastUsedUpdater struct {
	redis  lastUsedRedis
	repo   apiKeyLastUsedRepo
	logger *slog.Logger
}

// newLastUsedUpdater constructs a lastUsedUpdater. logger may be nil —
// slog.Default() is used in that case so the package doesn't panic on
// warn/info paths during dev-mode tests.
//
// rd may be nil (dev stacks that boot without Redis); Touch then runs the
// DB write inline on every call. In production Redis is required, but the
// debouncer never treats its absence as fatal because the debounce is an
// optimisation, not a security boundary.
func newLastUsedUpdater(rd lastUsedRedis, repo apiKeyLastUsedRepo, logger *slog.Logger) *lastUsedUpdater {
	if logger == nil {
		logger = slog.Default()
	}
	return &lastUsedUpdater{redis: rd, repo: repo, logger: logger}
}

// Touch fire-and-forget-updates last_used_at for the given key id. Returns
// immediately; the actual write happens on a goroutine so ValidateAPIKey
// stays hot. Callers MUST use a context that outlives the request —
// context.Background() is the intended choice; the request context would
// be cancelled the moment the client disconnects.
func (u *lastUsedUpdater) Touch(ctx context.Context, keyID uuid.UUID) {
	go u.touchNow(ctx, keyID)
}

// touchNow is called synchronously by tests + async by Touch. Exposed
// (lowercase) inside the package so unit tests can assert deterministic
// behaviour without racing a goroutine.
//
// Flow:
//  1. If Redis is available, SET NX the debounce key with a 5-minute TTL.
//     On success (SET NX returned true) we hold the write slot for this
//     window and continue to the DB write.
//     On MISS (SET NX returned false) we skip the DB write — another
//     tick already claimed this window.
//     On error (Redis down, network hiccup) we fail-OPEN and continue
//     to the DB write — losing the debounce for one call is preferable
//     to losing the write.
//  2. If Redis is not wired at all, jump straight to the DB write.
//  3. UpdateLastUsedAt errors are logged at Warn and swallowed — the
//     next ValidateAPIKey call will retry via its own debounce round.
func (u *lastUsedUpdater) touchNow(ctx context.Context, keyID uuid.UUID) {
	now := time.Now().UTC()

	// Nil-interface check: a typed nil (e.g. redisClient wrapping a nil
	// *redis.Client) would be non-nil at the interface level, so use the
	// two-value form via a nil-cmd fallback below.
	if u.redis != nil {
		key := "lastused:debounce:" + keyID.String()
		set, err := u.redis.SetNX(ctx, key, "1", lastUsedDebounceWindow).Result()
		if err == nil && !set {
			// Debounce wins: another request touched this key inside the
			// 5-min window. Skip the DB write.
			return
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			// Redis is down or slow — fail-OPEN. Fall through and run
			// the DB write inline. Log at Info (this is expected during
			// Redis restarts and shouldn't page).
			u.logger.Info("lastused debounce redis error; falling open", "err", err)
		}
	}

	if err := u.repo.UpdateLastUsedAt(ctx, keyID, now); err != nil {
		u.logger.Warn("lastused UPDATE failed", "key_id", keyID, "err", err)
	}
}
