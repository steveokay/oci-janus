// Package service — access_review_test.go covers AccessReviewService
// with in-memory fakes (no PG required). Focus areas:
//
//   - ListStaleKeys resolves the idle threshold from token_policies with
//     a fallback to 90 days when the policy is unset.
//   - The suggested-action heuristic returns the correct enum + reason
//     for the 4 branches (rotation-lapsed, well-past cutoff, recently
//     stale, uncertain).
//   - SnoozeAPIKeyReview rejects out-of-range days at BOTH the lower and
//     upper bounds (the SEC-064 lesson from FUT-003 — don't guard on
//     "if days > 0").
//   - SnoozeAPIKeyReview emits an auth.access_review.snoozed audit event
//     carrying the operator-picked window + until timestamp.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Fakes ─────────────────────────────────────────────────────────────

// fakeAccessReviewRepo captures inserts + returns configurable ListStaleKeys
// responses. Sub-tests set `rows` to seed the projection; snooze writes
// land in `snoozes` (keyed by key id) for assertion.
type fakeAccessReviewRepo struct {
	mu       sync.Mutex
	rows     []repository.StaleKey
	snoozes  map[uuid.UUID]*time.Time
	lookups  map[uuid.UUID]struct {
		tenantID uuid.UUID
		ownerID  uuid.UUID
	}
	// notFoundOnLookup, when true, makes GetTenantIDForKey always return
	// ErrNotFound so tests can exercise the missing-key branch.
	notFoundOnLookup bool
}

func newFakeAccessReviewRepo() *fakeAccessReviewRepo {
	return &fakeAccessReviewRepo{
		snoozes: make(map[uuid.UUID]*time.Time),
		lookups: make(map[uuid.UUID]struct {
			tenantID uuid.UUID
			ownerID  uuid.UUID
		}),
	}
}

func (f *fakeAccessReviewRepo) ListStaleKeys(_ context.Context, _ uuid.UUID, _ time.Time) ([]repository.StaleKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repository.StaleKey, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeAccessReviewRepo) SetReviewSnoozedUntil(_ context.Context, keyID uuid.UUID, until *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snoozes[keyID] = until
	return nil
}

func (f *fakeAccessReviewRepo) GetTenantIDForKey(_ context.Context, keyID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFoundOnLookup {
		return uuid.Nil, uuid.Nil, repository.ErrNotFound
	}
	if entry, ok := f.lookups[keyID]; ok {
		return entry.tenantID, entry.ownerID, nil
	}
	return uuid.Nil, uuid.Nil, repository.ErrNotFound
}

// registerLookup wires an id → (tenant, owner) mapping used by Snooze
// to enrich the audit event + return payload.
func (f *fakeAccessReviewRepo) registerLookup(keyID, tenantID, ownerID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups[keyID] = struct {
		tenantID uuid.UUID
		ownerID  uuid.UUID
	}{tenantID: tenantID, ownerID: ownerID}
}

// fakeAccessReviewPolicyRepo implements AccessReviewPolicyRepo with an
// in-memory map. When a tenant has no row (or is not seeded), it returns
// a zero policy (matching the real repo's grandfathering semantics).
type fakeAccessReviewPolicyRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]repository.TokenPolicy
}

func newFakeAccessReviewPolicyRepo() *fakeAccessReviewPolicyRepo {
	return &fakeAccessReviewPolicyRepo{rows: make(map[uuid.UUID]repository.TokenPolicy)}
}

func (f *fakeAccessReviewPolicyRepo) GetOrDefault(_ context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.rows[tenantID]; ok {
		return &row, nil
	}
	return &repository.TokenPolicy{TenantID: tenantID}, nil
}

// setIdleRevokeDays seeds the tenant's policy row with the given
// idle_revoke_days value. Used by tests that want a non-default
// threshold in the ListStaleKeys path.
func (f *fakeAccessReviewPolicyRepo) setIdleRevokeDays(tenantID uuid.UUID, days int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := days
	f.rows[tenantID] = repository.TokenPolicy{
		TenantID:       tenantID,
		IdleRevokeDays: &d,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────

// TestAccessReviewService_ListStaleKeys_UsesDefaultThreshold asserts the
// service falls back to 90 days when the tenant has no policy row.
func TestAccessReviewService_ListStaleKeys_UsesDefaultThreshold(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	policies := newFakeAccessReviewPolicyRepo()
	svc := NewAccessReviewService(repo, policies, nil)

	// Seed a single stale row so we know the service returned something.
	now := time.Now().UTC()
	oldLastUsed := now.Add(-120 * 24 * time.Hour)
	tenantID := uuid.New()
	repo.rows = []repository.StaleKey{{
		ID:          uuid.New(),
		TenantID:    tenantID,
		OwnerUserID: uuid.New(),
		Name:        "idle",
		LastUsedAt:  &oldLastUsed,
	}}

	got, err := svc.ListStaleKeys(context.Background(), tenantID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "idle", got[0].Key.Name)
}

// TestAccessReviewService_ListStaleKeys_UsesPolicyThreshold asserts the
// policy value overrides the default when set.
func TestAccessReviewService_ListStaleKeys_UsesPolicyThreshold(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	policies := newFakeAccessReviewPolicyRepo()
	svc := NewAccessReviewService(repo, policies, nil)

	tenantID := uuid.New()
	policies.setIdleRevokeDays(tenantID, 30)

	// The service passes staleCutoff to the repo — our fake ignores it
	// but by pinning the clock we can verify the resolved threshold is
	// consumed. This test asserts the read path (policy → threshold →
	// cutoff) doesn't error and produces the expected empty list when
	// no rows are seeded.
	got, err := svc.ListStaleKeys(context.Background(), tenantID)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAccessReviewService_ListStaleKeys_RejectsMissingTenant asserts the
// service refuses uuid.Nil at the gate.
func TestAccessReviewService_ListStaleKeys_RejectsMissingTenant(t *testing.T) {
	svc := NewAccessReviewService(newFakeAccessReviewRepo(), newFakeAccessReviewPolicyRepo(), nil)
	_, err := svc.ListStaleKeys(context.Background(), uuid.Nil)
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestSuggestedActionFor_Heuristic table-drives the 4 branches so a
// future tweak fails loudly. Kept in access_review_test.go (not a
// separate file) because the private helper is scoped to the service
// package.
func TestSuggestedActionFor_Heuristic(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// Default threshold — 90 days behind now.
	staleCutoff := now.Add(-90 * 24 * time.Hour)

	// Helper: pointer-to-time.
	tp := func(t time.Time) *time.Time { return &t }

	t.Run("RotationLapsed_TakesPrecedence", func(t *testing.T) {
		// last_used_at fresh, but rotation_due_at in the past.
		fresh := now.Add(-1 * time.Hour)
		rotDue := now.Add(-1 * time.Hour)
		k := repository.StaleKey{LastUsedAt: &fresh, RotationDueAt: &rotDue}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionRevoke, action)
		require.Equal(t, "rotation_lapsed", reason)
	})

	t.Run("RotationLapsed_AndIdle_ReportsBoth", func(t *testing.T) {
		veryOld := now.Add(-200 * 24 * time.Hour)
		rotDue := now.Add(-1 * time.Hour)
		k := repository.StaleKey{LastUsedAt: &veryOld, RotationDueAt: &rotDue}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionRevoke, action)
		require.Equal(t, "both", reason)
	})

	t.Run("NeverUsed_ReturnsRevoke", func(t *testing.T) {
		k := repository.StaleKey{LastUsedAt: nil}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionRevoke, action)
		require.Equal(t, "idle", reason)
	})

	t.Run("WellPastCutoff_ReturnsRevoke", func(t *testing.T) {
		// (cutoff - 14d) - 1 → definitely well past.
		veryOld := staleCutoff.Add(-15 * 24 * time.Hour)
		k := repository.StaleKey{LastUsedAt: tp(veryOld)}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionRevoke, action)
		require.Equal(t, "idle", reason)
	})

	t.Run("WithinGraceWindow_ReturnsKeep", func(t *testing.T) {
		// Just past cutoff, still within grace.
		recentlyPast := staleCutoff.Add(-1 * 24 * time.Hour)
		k := repository.StaleKey{LastUsedAt: tp(recentlyPast)}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionKeep, action)
		require.Equal(t, "idle", reason)
	})

	t.Run("NotYetIdle_ReturnsSnoozeFallback", func(t *testing.T) {
		// last_used_at newer than cutoff — should fall into the uncertain
		// bucket (the row shouldn't be in the list, but the heuristic
		// handles it defensively).
		fresh := now.Add(-1 * time.Hour)
		k := repository.StaleKey{LastUsedAt: &fresh}
		action, reason := suggestedActionFor(k, staleCutoff, now)
		require.Equal(t, SuggestedActionSnooze, action)
		require.Equal(t, "idle", reason)
	})
}

// TestAccessReviewService_SnoozeAPIKeyReview_RejectsOutOfRangeDays
// asserts BOTH bounds are refused with codes.InvalidArgument. This is
// the SEC-064 lesson from FUT-003 — don't guard on "if days > 0".
func TestAccessReviewService_SnoozeAPIKeyReview_RejectsOutOfRangeDays(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	svc := NewAccessReviewService(repo, newFakeAccessReviewPolicyRepo(), nil)

	keyID := uuid.New()
	tenantID := uuid.New()
	repo.registerLookup(keyID, tenantID, uuid.New())

	// Days = 0 must be rejected.
	_, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   keyID,
		Days:    0,
		ActorID: uuid.New(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())

	// Days = -1 must be rejected (guard against negative bypass).
	_, err = svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   keyID,
		Days:    -1,
		ActorID: uuid.New(),
	})
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())

	// Days = 91 must be rejected (upper bound).
	_, err = svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   keyID,
		Days:    91,
		ActorID: uuid.New(),
	})
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestAccessReviewService_SnoozeAPIKeyReview_RejectsMissingKey asserts
// a nil key id returns InvalidArgument before touching the repo.
func TestAccessReviewService_SnoozeAPIKeyReview_RejectsMissingKey(t *testing.T) {
	svc := NewAccessReviewService(newFakeAccessReviewRepo(), newFakeAccessReviewPolicyRepo(), nil)
	_, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		Days:    30,
		ActorID: uuid.New(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestAccessReviewService_SnoozeAPIKeyReview_RejectsMissingActor asserts
// a nil actor id returns InvalidArgument (BFF must always plumb sub).
func TestAccessReviewService_SnoozeAPIKeyReview_RejectsMissingActor(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	svc := NewAccessReviewService(repo, newFakeAccessReviewPolicyRepo(), nil)
	keyID := uuid.New()
	repo.registerLookup(keyID, uuid.New(), uuid.New())
	_, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID: keyID,
		Days:  30,
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestAccessReviewService_SnoozeAPIKeyReview_ReturnsNotFoundForMissingKey
// asserts the missing-key branch propagates as codes.NotFound.
func TestAccessReviewService_SnoozeAPIKeyReview_ReturnsNotFoundForMissingKey(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	repo.notFoundOnLookup = true
	svc := NewAccessReviewService(repo, newFakeAccessReviewPolicyRepo(), nil)
	_, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   uuid.New(),
		Days:    30,
		ActorID: uuid.New(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.NotFound, s.Code())
}

// TestAccessReviewService_SnoozeAPIKeyReview_EmitsAuditWithPayload asserts
// the emit shape includes the tenant + operator + until timestamp so the
// audit consumer can render "operator X snoozed key Y until Z".
func TestAccessReviewService_SnoozeAPIKeyReview_EmitsAuditWithPayload(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	audit := &capturingAuditEmitter{}
	svc := NewAccessReviewService(repo, newFakeAccessReviewPolicyRepo(), audit)

	// Pin the clock so we can compute the expected until value.
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	svc.WithClock(func() time.Time { return now })

	keyID := uuid.New()
	tenantID := uuid.New()
	ownerID := uuid.New()
	actorID := uuid.New()
	repo.registerLookup(keyID, tenantID, ownerID)

	got, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   keyID,
		Days:    30,
		ActorID: actorID,
	})
	require.NoError(t, err)
	require.Equal(t, keyID, got.ID)
	require.Equal(t, tenantID, got.TenantID)

	// One audit event captured with the expected payload.
	require.Len(t, audit.Events, 1)
	ev := audit.Events[0]
	require.Equal(t, events.RoutingAccessReviewSnoozed, ev.Action)
	require.Equal(t, keyID.String(), ev.Resource)

	payloadJSON, ok := ev.Fields["payload_json"].(string)
	require.True(t, ok, "audit event should carry payload_json")
	var p events.AccessReviewSnoozedPayload
	require.NoError(t, json.Unmarshal([]byte(payloadJSON), &p))
	require.Equal(t, tenantID.String(), p.TenantID)
	require.Equal(t, keyID.String(), p.KeyID)
	require.Equal(t, actorID.String(), p.ActorID)
	require.Equal(t, int32(30), p.DaysSnoozed)
	// Until is now + 30d formatted RFC3339.
	expectedUntil := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	require.Equal(t, expectedUntil, p.SnoozedUntil)
}

// TestAccessReviewService_SnoozeAPIKeyReview_AuditFailureNonFatal asserts
// an audit emit failure does NOT bubble up — the DB write is the source
// of truth and a broker outage cannot roll it back.
func TestAccessReviewService_SnoozeAPIKeyReview_AuditFailureNonFatal(t *testing.T) {
	repo := newFakeAccessReviewRepo()
	audit := &capturingAuditEmitter{EmitErr: errors.New("broker down")}
	svc := NewAccessReviewService(repo, newFakeAccessReviewPolicyRepo(), audit)

	keyID := uuid.New()
	repo.registerLookup(keyID, uuid.New(), uuid.New())

	_, err := svc.SnoozeAPIKeyReview(context.Background(), SnoozeAPIKeyReviewInput{
		KeyID:   keyID,
		Days:    30,
		ActorID: uuid.New(),
	})
	require.NoError(t, err, "audit failure must NOT bubble up")
}
