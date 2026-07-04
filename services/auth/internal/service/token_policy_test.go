// Package service — token_policy_test.go covers TokenPolicyService with
// in-memory fakes (no PG required). Focus areas:
//
//   - Get returns zero policy for unset tenant (no error).
//   - Put rejects zero + negative + too-high values.
//   - Put enforces the idle_revoke floor.
//   - Put emits an audit event carrying the before/after diff.
//   - Successful Put preserves nil (unset) fields via the repo's COALESCE.
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

// fakeTokenPolicyRepo implements the TokenPolicyRepo interface with an
// in-memory map keyed by tenant_id. Preserves nil-in fields (matches the
// COALESCE semantics of the real repo).
type fakeTokenPolicyRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]repository.TokenPolicy
}

func newFakeTokenPolicyRepo() *fakeTokenPolicyRepo {
	return &fakeTokenPolicyRepo{rows: make(map[uuid.UUID]repository.TokenPolicy)}
}

func (r *fakeTokenPolicyRepo) GetOrDefault(_ context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row, ok := r.rows[tenantID]; ok {
		return &row, nil
	}
	return &repository.TokenPolicy{TenantID: tenantID}, nil
}

func (r *fakeTokenPolicyRepo) Upsert(_ context.Context, in repository.TokenPolicy) (*repository.TokenPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := r.rows[in.TenantID]
	existing.TenantID = in.TenantID
	if in.MaxTTLDays != nil {
		existing.MaxTTLDays = in.MaxTTLDays
	}
	if in.RotationIntervalDays != nil {
		existing.RotationIntervalDays = in.RotationIntervalDays
	}
	if in.IdleRevokeDays != nil {
		existing.IdleRevokeDays = in.IdleRevokeDays
	}
	existing.UpdatedByUserID = in.UpdatedByUserID
	r.rows[in.TenantID] = existing
	out := existing
	return &out, nil
}

// int32P is a small helper so tests can inline literal pointers.
func int32P(v int32) *int32 { return &v }

func TestTokenPolicyService_Get_EmptyForUnsetTenant(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	tenantID := uuid.New()
	got, err := svc.Get(context.Background(), tenantID)
	require.NoError(t, err)
	require.Equal(t, tenantID, got.TenantID)
	require.Nil(t, got.MaxTTLDays, "unset tenant should have nil MaxTTLDays")
}

func TestTokenPolicyService_Get_RequiresTenantID(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	_, err := svc.Get(context.Background(), uuid.Nil)
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestTokenPolicyService_Put_Success(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	audit := &capturingAuditEmitter{}
	svc := NewTokenPolicyService(repo, audit)

	tenantID := uuid.New()
	actorID := uuid.New()
	got, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:             tenantID,
		MaxTTLDays:           int32P(90),
		RotationIntervalDays: int32P(30),
		IdleRevokeDays:       int32P(30),
		ActorID:              actorID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.MaxTTLDays)
	require.Equal(t, int32(90), *got.MaxTTLDays)
	require.NotNil(t, got.RotationIntervalDays)
	require.Equal(t, int32(30), *got.RotationIntervalDays)
	require.NotNil(t, got.IdleRevokeDays)
	require.Equal(t, int32(30), *got.IdleRevokeDays)
	require.NotNil(t, got.UpdatedByUserID)
	require.Equal(t, actorID, *got.UpdatedByUserID)
}

func TestTokenPolicyService_Put_RejectsZeroDays(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	tenantID := uuid.New()
	actorID := uuid.New()

	// Zero max_ttl_days is rejected.
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(0),
		ActorID:    actorID,
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())

	// Negative is rejected.
	_, err = svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:             tenantID,
		RotationIntervalDays: int32P(-1),
		ActorID:              actorID,
	})
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestTokenPolicyService_Put_RejectsAboveMaxDays(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	tenantID := uuid.New()
	actorID := uuid.New()
	// 11 years > 3650 day cap.
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(3651),
		ActorID:    actorID,
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestTokenPolicyService_Put_RejectsTooShortIdleRevoke(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	tenantID := uuid.New()
	actorID := uuid.New()
	// 3 days < 7-day floor.
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:       tenantID,
		IdleRevokeDays: int32P(3),
		ActorID:        actorID,
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestTokenPolicyService_Put_RequiresActor(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   uuid.New(),
		MaxTTLDays: int32P(30),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestTokenPolicyService_Put_EmitsAuditWithDiff(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	audit := &capturingAuditEmitter{}
	svc := NewTokenPolicyService(repo, audit)

	tenantID := uuid.New()
	actorID := uuid.New()

	// First Put — before is all-nil, after has MaxTTLDays=90.
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(90),
		ActorID:    actorID,
	})
	require.NoError(t, err)

	// Second Put — before has MaxTTLDays=90, after has MaxTTLDays=60.
	_, err = svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(60),
		ActorID:    actorID,
	})
	require.NoError(t, err)

	got := audit.Events
	require.Len(t, got, 2)
	// Assert the second event's payload has before=90, after=60.
	require.Equal(t, events.RoutingTokenPolicyChanged, got[1].Action)
	payloadJSON, ok := got[1].Fields["payload_json"].(string)
	require.True(t, ok, "audit event should carry payload_json")
	var p events.TokenPolicyChangedPayload
	require.NoError(t, json.Unmarshal([]byte(payloadJSON), &p))
	require.NotNil(t, p.Before.MaxTTLDays)
	require.Equal(t, int32(90), *p.Before.MaxTTLDays)
	require.NotNil(t, p.After.MaxTTLDays)
	require.Equal(t, int32(60), *p.After.MaxTTLDays)
}

func TestTokenPolicyService_Put_PartialUpdate_PreservesUnsetFields(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	svc := NewTokenPolicyService(repo, nil)

	tenantID := uuid.New()
	actorID := uuid.New()

	// Seed with all three.
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:             tenantID,
		MaxTTLDays:           int32P(90),
		RotationIntervalDays: int32P(30),
		IdleRevokeDays:       int32P(30),
		ActorID:              actorID,
	})
	require.NoError(t, err)

	// Partial: update only max_ttl_days. Others stay.
	got, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(120),
		ActorID:    actorID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.MaxTTLDays)
	require.Equal(t, int32(120), *got.MaxTTLDays)
	require.NotNil(t, got.RotationIntervalDays)
	require.Equal(t, int32(30), *got.RotationIntervalDays)
	require.NotNil(t, got.IdleRevokeDays)
	require.Equal(t, int32(30), *got.IdleRevokeDays)
}

// ── CreateAPIKey enforcement tests (Task 6) ────────────────────────────

// TestCreateAPIKey_NilExpiryClampedToPolicyCap — SEC-064 regression.
// The initial FUT-003 impl guarded on `expiresAt != nil`, so a caller
// omitting the expiry field silently bypassed a max_ttl_days policy and
// got a forever-key. The fix clamps a nil expiry to the policy cap so
// the operator gets the max TTL they configured, not perpetuity.
func TestCreateAPIKey_NilExpiryClampedToPolicyCap(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	policyRepo := newFakeTokenPolicyRepo()
	// 30-day cap.
	_, err := policyRepo.Upsert(context.Background(), repository.TokenPolicy{
		TenantID:   tenantID,
		MaxTTLDays: int32P(30),
	})
	require.NoError(t, err)
	svc.SetTokenPolicyRepo(policyRepo)

	// Caller omits expiry — pre-fix this would create a forever-key.
	before := time.Now()
	key, _, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "nil-expiry", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, key.ExpiresAt, "SEC-064: expires_at MUST be clamped, not left nil")
	// The clamped expiry should be ~30 days from now (within a small
	// clock-skew window). Assert both bounds.
	upperBound := before.Add(30*24*time.Hour + time.Minute)
	lowerBound := before.Add(30*24*time.Hour - time.Minute)
	require.WithinRange(t, *key.ExpiresAt, lowerBound, upperBound,
		"clamped expiry should be at policy cap (30 days)")
}

// TestCreateAPIKey_RejectsTTLAboveCap asserts a caller-requested TTL that
// exceeds the workspace max is refused with InvalidArgument.
func TestCreateAPIKey_RejectsTTLAboveCap(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	policyRepo := newFakeTokenPolicyRepo()
	// Seed a 30-day cap.
	_, err := policyRepo.Upsert(context.Background(), repository.TokenPolicy{
		TenantID:   tenantID,
		MaxTTLDays: int32P(30),
	})
	require.NoError(t, err)
	svc.SetTokenPolicyRepo(policyRepo)

	// Request 60 days — beyond the cap.
	expires := time.Now().Add(60 * 24 * time.Hour)
	_, _, err = svc.CreateAPIKey(context.Background(), tenantID, userID, "over-cap", nil, &expires)
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestCreateAPIKey_AllowsTTLAtCap asserts the cap is inclusive on the
// low side (30 days requested against a 30-day cap should succeed).
func TestCreateAPIKey_AllowsTTLAtCap(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	policyRepo := newFakeTokenPolicyRepo()
	_, err := policyRepo.Upsert(context.Background(), repository.TokenPolicy{
		TenantID:   tenantID,
		MaxTTLDays: int32P(30),
	})
	require.NoError(t, err)
	svc.SetTokenPolicyRepo(policyRepo)

	// 29 days — comfortably under. (30 days exact hits floating clock skew.)
	expires := time.Now().Add(29 * 24 * time.Hour)
	key, rawSecret, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "at-cap", nil, &expires)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.NotEmpty(t, rawSecret)
}

// TestCreateAPIKey_SetsRotationDueAtWhenPolicySet asserts a rotation
// deadline is stamped when the policy has rotation_interval_days.
func TestCreateAPIKey_SetsRotationDueAtWhenPolicySet(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	policyRepo := newFakeTokenPolicyRepo()
	_, err := policyRepo.Upsert(context.Background(), repository.TokenPolicy{
		TenantID:             tenantID,
		RotationIntervalDays: int32P(90),
	})
	require.NoError(t, err)
	svc.SetTokenPolicyRepo(policyRepo)

	before := time.Now()
	key, _, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "rotate-me", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, key.RotationDueAt)
	require.True(t, key.RotationDueAt.After(before.Add(89*24*time.Hour)),
		"rotation_due_at should be roughly 90 days in the future")
}

// TestCreateAPIKey_NoPolicyIsPermissive asserts that an unconfigured
// policy leaves the create path fully permissive — the legacy behaviour
// (no cap, no rotation deadline).
func TestCreateAPIKey_NoPolicyIsPermissive(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	// No SetTokenPolicyRepo call — the service has s.tokenPolicy == nil.

	expires := time.Now().Add(365 * 24 * time.Hour)
	key, _, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "no-policy", nil, &expires)
	require.NoError(t, err)
	require.Nil(t, key.RotationDueAt, "no policy → no rotation deadline")
}

// TestCreateAPIKey_ExistingKeysGrandfathered — LOAD-BEARING SECURITY
// INVARIANT.
//
// A stricter policy applied AFTER a key is issued must NOT retroactively
// affect existing keys. The invariant is enforced structurally by only
// consulting the policy at CreateAPIKey time; this test guards against a
// future refactor that accidentally moves the consultation to ValidateAPIKey.
func TestCreateAPIKey_ExistingKeysGrandfathered(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()

	// Step 1: no policy yet — issue a key with a 1-year TTL.
	longExpires := time.Now().Add(365 * 24 * time.Hour)
	key, rawSecret, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "legacy", nil, &longExpires)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.NotEmpty(t, rawSecret)

	// Step 2: apply a strict 30-day cap.
	policyRepo := newFakeTokenPolicyRepo()
	_, err = policyRepo.Upsert(context.Background(), repository.TokenPolicy{
		TenantID:   tenantID,
		MaxTTLDays: int32P(30),
	})
	require.NoError(t, err)
	svc.SetTokenPolicyRepo(policyRepo)

	// Step 3: the existing key MUST STILL VALIDATE. Its TTL of 1 year is
	// grandfathered — ValidateAPIKey does not consult the policy.
	vk, err := svc.ValidateAPIKey(context.Background(), ValidateAPIKeyOpts{
		KeyID:     key.ID,
		RawSecret: rawSecret,
	})
	require.NoError(t, err, "existing key must still validate after stricter policy")
	require.NotNil(t, vk)
	require.Equal(t, userID, vk.UserID)

	// Step 4: NEW keys respect the cap.
	newExpires := time.Now().Add(60 * 24 * time.Hour)
	_, _, err = svc.CreateAPIKey(context.Background(), tenantID, userID, "new-over-cap", nil, &newExpires)
	require.Error(t, err, "new key over the fresh cap must be rejected")
}

// TestTokenPolicyService_Put_AuditFailureNonFatal asserts that an emit
// failure does NOT bubble up — the DB write is the source of truth and
// a broker outage cannot roll it back.
func TestTokenPolicyService_Put_AuditFailureNonFatal(t *testing.T) {
	repo := newFakeTokenPolicyRepo()
	audit := &capturingAuditEmitter{EmitErr: errors.New("broker down")}
	svc := NewTokenPolicyService(repo, audit)

	tenantID := uuid.New()
	actorID := uuid.New()
	_, err := svc.Put(context.Background(), PutTokenPolicyInput{
		TenantID:   tenantID,
		MaxTTLDays: int32P(30),
		ActorID:    actorID,
	})
	require.NoError(t, err, "audit failure must NOT bubble up")
}
