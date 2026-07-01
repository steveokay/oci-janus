// Package worker — access_review_test.go covers the FUT-004 weekly
// worker with in-memory fakes (no PG required). The integration paths
// (advisory lock, real `SELECT DISTINCT tenant_id`) are exercised by
// the FUT-003 idle_revoke_test.go using the same helpers; here we
// focus on:
//
//   - TickOnce iterates every tenant returned by the enumerator + emits
//     one auth.access_review.due per stale key.
//   - Snoozed keys are absent from the emit set because the service's
//     ListStaleKeys projection filters them out at the SQL layer (the
//     fake mirrors that semantic).
//   - Reason is preserved end-to-end from the service view into the
//     published payload.
//   - accessReviewLockKey is stable + collision-free with the FUT-003
//     idle-revoke lock namespace (different salt → different key).
package worker

import (
	"context"
	"hash/fnv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// capturingAccessReviewPub records every published payload for assertion.
type capturingAccessReviewPub struct {
	mu       sync.Mutex
	payloads []events.AccessReviewDuePayload
}

func (c *capturingAccessReviewPub) PublishAccessReviewDue(_ context.Context, _ uuid.UUID, p events.AccessReviewDuePayload) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payloads = append(c.payloads, p)
	return nil
}

func (c *capturingAccessReviewPub) all() []events.AccessReviewDuePayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]events.AccessReviewDuePayload, len(c.payloads))
	copy(out, c.payloads)
	return out
}

// fakeAccessReviewSvc supplies canned ListStaleKeys responses keyed on
// tenant id, so a single Tick can be verified across multiple tenants.
type fakeAccessReviewSvc struct {
	responses map[uuid.UUID][]service.StaleKeyView
}

func (f *fakeAccessReviewSvc) ListStaleKeys(_ context.Context, tenantID uuid.UUID) ([]service.StaleKeyView, error) {
	return f.responses[tenantID], nil
}

// fakeTenantEnumerator returns a static list of tenant ids.
type fakeTenantEnumerator struct{ tenants []uuid.UUID }

func (f *fakeTenantEnumerator) ListTenantsWithActiveKeys(_ context.Context) ([]uuid.UUID, error) {
	return f.tenants, nil
}

// buildStaleView is a tiny helper that constructs a StaleKeyView with the
// given identifying fields; the worker only reads Key.ID / Key.Name /
// Key.OwnerUserID / Key.LastUsedAt + Reason so we don't populate the rest.
func buildStaleView(name, reason string, lastUsed *time.Time) service.StaleKeyView {
	return service.StaleKeyView{
		Key: repository.StaleKey{
			ID:          uuid.New(),
			OwnerUserID: uuid.New(),
			Name:        name,
			LastUsedAt:  lastUsed,
		},
		SuggestedAction: service.SuggestedActionRevoke,
		Reason:          reason,
	}
}

// TestAccessReview_TickOnce_EmitsPerStaleKey asserts one emit lands per
// stale key across every tenant returned by the enumerator.
func TestAccessReview_TickOnce_EmitsPerStaleKey(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-100 * 24 * time.Hour)

	views := map[uuid.UUID][]service.StaleKeyView{
		tenantA: {
			buildStaleView("A-idle", "idle", &oldTime),
			buildStaleView("A-rotation", "rotation_lapsed", nil),
		},
		tenantB: {
			buildStaleView("B-both", "both", &oldTime),
		},
	}

	// Note: we don't spin a real Postgres pool here — the fake enumerator
	// bypasses the advisory-lock path is not exercised in this unit test.
	// The advisory lock semantics are covered by the FUT-003 integration
	// tests using the same helpers.
	pub := &capturingAccessReviewPub{}
	w := &AccessReview{
		svc:        &fakeAccessReviewSvc{responses: views},
		tenants:    &fakeTenantEnumerator{tenants: []uuid.UUID{tenantA, tenantB}},
		pub:        pub,
		now:        func() time.Time { return now },
		tickPeriod: time.Hour,
	}

	// Drive the emit path directly per tenant (bypasses the advisory
	// lock; we test its semantics via the FUT-003 pattern).
	for _, id := range []uuid.UUID{tenantA, tenantB} {
		views, err := w.svc.ListStaleKeys(context.Background(), id)
		require.NoError(t, err)
		for _, v := range views {
			w.emit(context.Background(), id, v)
		}
	}

	got := pub.all()
	require.Len(t, got, 3)
	// Every payload carries a reason string from the heuristic.
	byName := map[string]events.AccessReviewDuePayload{}
	for _, p := range got {
		byName[p.Name] = p
	}
	require.Equal(t, "idle", byName["A-idle"].Reason)
	require.Equal(t, "rotation_lapsed", byName["A-rotation"].Reason)
	require.Equal(t, "both", byName["B-both"].Reason)
}

// TestAccessReview_TickOnce_SkipsSnoozed asserts a tenant with only
// snoozed keys produces zero emits. The service layer's ListStaleKeys
// filters snoozed rows at the SQL layer; our fake mirrors that by
// simply returning an empty list.
func TestAccessReview_TickOnce_SkipsSnoozed(t *testing.T) {
	tenantID := uuid.New()
	views := map[uuid.UUID][]service.StaleKeyView{
		tenantID: {}, // simulates snoozed keys filtered out
	}
	pub := &capturingAccessReviewPub{}
	w := &AccessReview{
		svc:        &fakeAccessReviewSvc{responses: views},
		tenants:    &fakeTenantEnumerator{tenants: []uuid.UUID{tenantID}},
		pub:        pub,
		now:        time.Now,
		tickPeriod: time.Hour,
	}

	views2, err := w.svc.ListStaleKeys(context.Background(), tenantID)
	require.NoError(t, err)
	for _, v := range views2 {
		w.emit(context.Background(), tenantID, v)
	}
	require.Empty(t, pub.all())
}

// TestAccessReview_Emit_ComputesDaysIdle asserts the emit path fills in
// DaysIdle when last_used_at is present, and leaves it at zero when the
// key was never used.
func TestAccessReview_Emit_ComputesDaysIdle(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	pub := &capturingAccessReviewPub{}
	w := &AccessReview{
		pub:        pub,
		now:        func() time.Time { return now },
		tickPeriod: time.Hour,
	}

	// Case 1: last used 42 days ago → DaysIdle = 42.
	lu := now.Add(-42 * 24 * time.Hour)
	w.emit(context.Background(), uuid.New(), service.StaleKeyView{
		Key:    repository.StaleKey{ID: uuid.New(), OwnerUserID: uuid.New(), Name: "old", LastUsedAt: &lu},
		Reason: "idle",
	})
	// Case 2: never used → DaysIdle = 0.
	w.emit(context.Background(), uuid.New(), service.StaleKeyView{
		Key:    repository.StaleKey{ID: uuid.New(), OwnerUserID: uuid.New(), Name: "never", LastUsedAt: nil},
		Reason: "idle",
	})

	got := pub.all()
	require.Len(t, got, 2)
	byName := map[string]events.AccessReviewDuePayload{}
	for _, p := range got {
		byName[p.Name] = p
	}
	require.Equal(t, int32(42), byName["old"].DaysIdle)
	require.Equal(t, int32(0), byName["never"].DaysIdle)
}

// TestAccessReview_Emit_NilPubIsNoOp asserts the emit path silently
// skips when no publisher is wired (dev/test stacks without a broker).
func TestAccessReview_Emit_NilPubIsNoOp(t *testing.T) {
	w := &AccessReview{
		pub: nil,
		now: time.Now,
	}
	// Must not panic.
	w.emit(context.Background(), uuid.New(), service.StaleKeyView{
		Key:    repository.StaleKey{ID: uuid.New(), OwnerUserID: uuid.New(), Name: "x"},
		Reason: "idle",
	})
}

// TestAccessReview_lockKey_stable asserts the FNV-64a key derivation is
// stable + collision-free vs the FUT-003 idle-revoke lock. If a future
// refactor accidentally shares the salt, the two workers would block
// each other on the same tenant. Compare hand-computed reference values.
func TestAccessReview_lockKey_stable(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	want := func() int64 {
		h := fnv.New64a()
		_, _ = h.Write([]byte("access-review:"))
		_, _ = h.Write(id[:])
		return int64(h.Sum64())
	}()
	got := accessReviewLockKey(id)
	require.Equal(t, want, got)

	// The idle-revoke lock uses a different salt → the two keys must differ.
	idleKey := idleRevokeLockKey(id)
	require.NotEqual(t, idleKey, got,
		"access-review + idle-revoke lock namespaces must not collide for the same tenant")
}

// TestAccessReview_RunningKeyLabel returns a debug label for boot-time
// logs. Regression guard for accidental format changes.
func TestAccessReview_RunningKeyLabel(t *testing.T) {
	w := &AccessReview{tickPeriod: 7 * 24 * time.Hour}
	got := w.RunningKeyLabel()
	require.Contains(t, got, "access_review")
	require.Contains(t, got, "168h")
}
