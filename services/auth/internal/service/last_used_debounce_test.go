// Package service — last_used_debounce_test.go covers the FUT-003
// Redis-debounced last_used_at updater.
//
// Sub-tests cover the four operational modes:
//   - FirstCallWins           — SET NX succeeds → UPDATE runs.
//   - SecondCallWithinWindowSkipped — SET NX fails → UPDATE does NOT run.
//   - SecondCallAfterWindowRuns — window expires → UPDATE runs again.
//   - RedisDown_FailOpen      — Redis closed → UPDATE runs inline.
//   - UpdateFailureIsLoggedNotFatal — repo returns error → no panic, no
//     debounce roll-back (the debounce still holds; retry storms are avoided).
package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// fakeLastUsedRepo counts UpdateLastUsedAt calls + records the last id it
// saw. Errors on demand for the fail-loudly path.
type fakeLastUsedRepo struct {
	mu    sync.Mutex
	calls int
	last  uuid.UUID
	// err, if non-nil, is returned from every UpdateLastUsedAt call.
	err error
}

func (f *fakeLastUsedRepo) UpdateLastUsedAt(_ context.Context, id uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = id
	return f.err
}

func (f *fakeLastUsedRepo) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestLastUsedUpdater_FirstCallWins asserts that the first Touch for a
// given key_id runs the DB update.
func TestLastUsedUpdater_FirstCallWins(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := &fakeLastUsedRepo{}
	u := newLastUsedUpdater(rdb, repo, slog.Default())

	keyID := uuid.New()
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls(), "first Touch should invoke UpdateLastUsedAt")
	require.Equal(t, keyID, repo.last)
}

// TestLastUsedUpdater_SecondCallWithinWindowSkipped verifies the debounce:
// a second Touch inside the 5-minute window MUST skip the DB write.
func TestLastUsedUpdater_SecondCallWithinWindowSkipped(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := &fakeLastUsedRepo{}
	u := newLastUsedUpdater(rdb, repo, slog.Default())

	keyID := uuid.New()
	u.touchNow(context.Background(), keyID)
	u.touchNow(context.Background(), keyID)
	u.touchNow(context.Background(), keyID)

	require.Equal(t, 1, repo.Calls(), "second Touch inside window should be debounced")
}

// TestLastUsedUpdater_SecondCallAfterWindowRuns asserts the window is
// finite: after the debounce key expires, the next Touch re-runs.
func TestLastUsedUpdater_SecondCallAfterWindowRuns(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := &fakeLastUsedRepo{}
	u := newLastUsedUpdater(rdb, repo, slog.Default())

	keyID := uuid.New()
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls())

	// miniredis lets us fast-forward the TTL clock so we don't need to
	// sleep 5 minutes inside a unit test.
	mr.FastForward(lastUsedDebounceWindow + time.Second)

	u.touchNow(context.Background(), keyID)
	require.Equal(t, 2, repo.Calls(), "post-window Touch should re-run UpdateLastUsedAt")
}

// TestLastUsedUpdater_RedisDown_FailOpen asserts that a dead Redis does
// NOT stop the write path — the debounce is an optimisation, not a
// security boundary.
func TestLastUsedUpdater_RedisDown_FailOpen(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Close miniredis before touching — every command errors.
	mr.Close()

	repo := &fakeLastUsedRepo{}
	u := newLastUsedUpdater(rdb, repo, slog.Default())

	keyID := uuid.New()
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls(), "Redis-down should fail open and run the DB write")
}

// TestLastUsedUpdater_NilRedis_RunsInline asserts that when the service
// is constructed without a Redis client (dev-mode), Touch still runs the
// DB write directly.
func TestLastUsedUpdater_NilRedis_RunsInline(t *testing.T) {
	repo := &fakeLastUsedRepo{}
	u := newLastUsedUpdater(nil, repo, slog.Default())

	keyID := uuid.New()
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls(), "nil Redis should still run the DB write inline")
}

// TestLastUsedUpdater_UpdateFailureIsLoggedNotFatal asserts that a DB
// update failure is swallowed — the debounce still holds so we don't
// retry-storm the DB.
func TestLastUsedUpdater_UpdateFailureIsLoggedNotFatal(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := &fakeLastUsedRepo{err: errors.New("boom")}
	u := newLastUsedUpdater(rdb, repo, slog.Default())

	keyID := uuid.New()
	// Must not panic.
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls(), "first attempt was made even though it failed")

	// The debounce key should now be set — a follow-up call skips the
	// DB write so a broken repo doesn't get hammered with retries.
	u.touchNow(context.Background(), keyID)
	require.Equal(t, 1, repo.Calls(), "second call within window should still debounce despite failure")
}
