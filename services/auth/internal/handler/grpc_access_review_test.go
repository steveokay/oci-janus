// Package handler — grpc_access_review_test.go covers the FUT-004 gRPC
// handlers. Uses tiny in-memory fakes so the tests don't need a real
// DB. The service-layer tests exercise the full heuristic + validation
// contract; these tests focus on the gRPC-layer projection (proto
// mapping, error codes, unimplemented gates).
package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// arTestRepo is a tiny in-memory AccessReviewRepo used by handler tests.
// Sub-tests seed `rows` before calling ListStaleKeys and register a key
// via registerLookup before calling Snooze.
type arTestRepo struct {
	mu      sync.Mutex
	rows    []repository.StaleKey
	lookups map[uuid.UUID]struct {
		tenantID uuid.UUID
		ownerID  uuid.UUID
	}
}

func newARTestRepo() *arTestRepo {
	return &arTestRepo{lookups: make(map[uuid.UUID]struct {
		tenantID uuid.UUID
		ownerID  uuid.UUID
	})}
}

func (f *arTestRepo) ListStaleKeys(_ context.Context, _ uuid.UUID, _ time.Time) ([]repository.StaleKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repository.StaleKey, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *arTestRepo) SetReviewSnoozedUntil(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return nil
}

func (f *arTestRepo) GetTenantIDForKey(_ context.Context, keyID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if entry, ok := f.lookups[keyID]; ok {
		return entry.tenantID, entry.ownerID, nil
	}
	return uuid.Nil, uuid.Nil, repository.ErrNotFound
}

func (f *arTestRepo) registerLookup(keyID, tenantID, ownerID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups[keyID] = struct {
		tenantID uuid.UUID
		ownerID  uuid.UUID
	}{tenantID: tenantID, ownerID: ownerID}
}

// arTestPolicyRepo returns zero policies (unset) so the service falls
// through to the default 90-day threshold. Sufficient for handler
// tests — service-layer tests exercise the policy-set path.
type arTestPolicyRepo struct{}

func (arTestPolicyRepo) GetOrDefault(_ context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error) {
	return &repository.TokenPolicy{TenantID: tenantID}, nil
}

// TestGRPCHandler_ListStaleKeys_UnimplementedWhenNotWired asserts the
// codes.Unimplemented gate fires when WithAccessReviewService was never
// called.
func TestGRPCHandler_ListStaleKeys_UnimplementedWhenNotWired(t *testing.T) {
	h := &GRPCHandler{}
	_, err := h.ListStaleKeys(context.Background(), &authv1.ListStaleKeysRequest{TenantId: uuid.NewString()})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.Unimplemented, s.Code())
}

// TestGRPCHandler_ListStaleKeys_InvalidTenant asserts a malformed
// tenant_id lands as codes.InvalidArgument before any repo call.
func TestGRPCHandler_ListStaleKeys_InvalidTenant(t *testing.T) {
	repo := newARTestRepo()
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)
	_, err := h.ListStaleKeys(context.Background(), &authv1.ListStaleKeysRequest{TenantId: "not-a-uuid"})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestGRPCHandler_ListStaleKeys_ProjectsHeuristicIntoProto asserts the
// gRPC layer converts service.StaleKeyView → authv1.StaleKey with the
// correct SuggestedAction enum + reason string.
func TestGRPCHandler_ListStaleKeys_ProjectsHeuristicIntoProto(t *testing.T) {
	tenantID := uuid.New()
	ownerID := uuid.New()
	// Well-past-cutoff idle key so the service surfaces it AND the
	// heuristic returns SUGGESTED_ACTION_REVOKE with reason="idle".
	old := time.Now().UTC().Add(-200 * 24 * time.Hour)
	repo := newARTestRepo()
	repo.rows = []repository.StaleKey{{
		ID:          uuid.New(),
		TenantID:    tenantID,
		OwnerUserID: ownerID,
		Name:        "old-bot",
		LastUsedAt:  &old,
	}}
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)

	got, err := h.ListStaleKeys(context.Background(), &authv1.ListStaleKeysRequest{TenantId: tenantID.String()})
	require.NoError(t, err)
	require.Len(t, got.GetKeys(), 1)
	first := got.GetKeys()[0]
	require.Equal(t, "old-bot", first.GetName())
	require.Equal(t, tenantID.String(), first.GetTenantId())
	require.Equal(t, ownerID.String(), first.GetOwnerUserId())
	require.Equal(t, authv1.SuggestedAction_SUGGESTED_ACTION_REVOKE, first.GetSuggestedAction())
	require.Equal(t, "idle", first.GetReason())
	require.NotNil(t, first.GetLastUsedAt(), "last_used_at must be populated when set")
	require.Nil(t, first.GetRotationDueAt(), "rotation_due_at must remain nil when unset")
}

// TestGRPCHandler_SnoozeAPIKeyReview_UnimplementedWhenNotWired asserts
// the codes.Unimplemented gate fires when the service isn't wired.
func TestGRPCHandler_SnoozeAPIKeyReview_UnimplementedWhenNotWired(t *testing.T) {
	h := &GRPCHandler{}
	_, err := h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   uuid.NewString(),
		Days:    30,
		ActorId: uuid.NewString(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.Unimplemented, s.Code())
}

// TestGRPCHandler_SnoozeAPIKeyReview_HappyPath asserts the round-trip
// returns a StaleKey with the ID + tenant + snoozed_until fields set.
func TestGRPCHandler_SnoozeAPIKeyReview_HappyPath(t *testing.T) {
	repo := newARTestRepo()
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)

	keyID := uuid.New()
	tenantID := uuid.New()
	ownerID := uuid.New()
	repo.registerLookup(keyID, tenantID, ownerID)

	got, err := h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   keyID.String(),
		Days:    30,
		ActorId: uuid.NewString(),
	})
	require.NoError(t, err)
	require.Equal(t, keyID.String(), got.GetId())
	require.Equal(t, tenantID.String(), got.GetTenantId())
	require.Equal(t, ownerID.String(), got.GetOwnerUserId())
	require.NotNil(t, got.GetReviewSnoozedUntil(), "snooze timestamp must be populated in response")
}

// TestGRPCHandler_SnoozeAPIKeyReview_InvalidKeyID asserts a malformed
// key_id lands as InvalidArgument.
func TestGRPCHandler_SnoozeAPIKeyReview_InvalidKeyID(t *testing.T) {
	repo := newARTestRepo()
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)
	_, err := h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   "not-a-uuid",
		Days:    30,
		ActorId: uuid.NewString(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestGRPCHandler_SnoozeAPIKeyReview_InvalidActorID asserts a malformed
// actor_id lands as InvalidArgument.
func TestGRPCHandler_SnoozeAPIKeyReview_InvalidActorID(t *testing.T) {
	repo := newARTestRepo()
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)
	_, err := h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   uuid.NewString(),
		Days:    30,
		ActorId: "not-a-uuid",
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

// TestGRPCHandler_SnoozeAPIKeyReview_OutOfRangeDaysPropagates asserts
// the service-layer InvalidArgument (from days bound-check) surfaces
// through the handler with the same code. Defence-in-depth guard so a
// caller can't bypass the bound by hitting a different code path.
func TestGRPCHandler_SnoozeAPIKeyReview_OutOfRangeDaysPropagates(t *testing.T) {
	repo := newARTestRepo()
	svc := service.NewAccessReviewService(repo, arTestPolicyRepo{}, nil)
	h := (&GRPCHandler{}).WithAccessReviewService(svc)
	keyID := uuid.New()
	repo.registerLookup(keyID, uuid.New(), uuid.New())

	// Zero days must be rejected.
	_, err := h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   keyID.String(),
		Days:    0,
		ActorId: uuid.NewString(),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())

	// 91 days must be rejected.
	_, err = h.SnoozeAPIKeyReview(context.Background(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   keyID.String(),
		Days:    91,
		ActorId: uuid.NewString(),
	})
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}
