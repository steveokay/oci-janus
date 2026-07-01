// Package handler — grpc_token_policy_test.go covers the FUT-003 gRPC
// handlers. Uses an in-memory TokenPolicyRepo fake so the test doesn't
// need a real DB.
package handler

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// tpTestRepo is a tiny in-memory TokenPolicyRepo used by handler tests.
// Sufficient for the gRPC-layer contract; the service-layer validation
// tests exercise the full COALESCE / partial-update semantics.
type tpTestRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]repository.TokenPolicy
}

func newTPTestRepo() *tpTestRepo {
	return &tpTestRepo{rows: make(map[uuid.UUID]repository.TokenPolicy)}
}

func (r *tpTestRepo) GetOrDefault(_ context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row, ok := r.rows[tenantID]; ok {
		return &row, nil
	}
	return &repository.TokenPolicy{TenantID: tenantID}, nil
}

func (r *tpTestRepo) Upsert(_ context.Context, in repository.TokenPolicy) (*repository.TokenPolicy, error) {
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

func TestGRPCHandler_GetTokenPolicy_UnimplementedWhenNotWired(t *testing.T) {
	h := &GRPCHandler{}
	_, err := h.GetTokenPolicy(context.Background(), &authv1.GetTokenPolicyRequest{TenantId: uuid.NewString()})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.Unimplemented, s.Code())
}

func TestGRPCHandler_GetTokenPolicy_HappyPath(t *testing.T) {
	repo := newTPTestRepo()
	svc := service.NewTokenPolicyService(repo, nil)
	h := (&GRPCHandler{}).WithTokenPolicyService(svc)

	tenantID := uuid.New()
	got, err := h.GetTokenPolicy(context.Background(), &authv1.GetTokenPolicyRequest{TenantId: tenantID.String()})
	require.NoError(t, err)
	require.Equal(t, tenantID.String(), got.GetTenantId())
	require.Nil(t, got.GetMaxTtlDays(), "unset tenant → nil wrapper")
}

func TestGRPCHandler_PutTokenPolicy_HappyPath(t *testing.T) {
	repo := newTPTestRepo()
	svc := service.NewTokenPolicyService(repo, nil)
	h := (&GRPCHandler{}).WithTokenPolicyService(svc)

	tenantID := uuid.New()
	actorID := uuid.New()
	got, err := h.PutTokenPolicy(context.Background(), &authv1.PutTokenPolicyRequest{
		TenantId:             tenantID.String(),
		MaxTtlDays:           wrapperspb.Int32(90),
		RotationIntervalDays: wrapperspb.Int32(30),
		IdleRevokeDays:       wrapperspb.Int32(30),
		ActorId:              actorID.String(),
	})
	require.NoError(t, err)
	require.Equal(t, int32(90), got.GetMaxTtlDays().GetValue())
	require.Equal(t, int32(30), got.GetRotationIntervalDays().GetValue())
	require.Equal(t, int32(30), got.GetIdleRevokeDays().GetValue())
	require.Equal(t, actorID.String(), got.GetUpdatedByUserId())
}

func TestGRPCHandler_PutTokenPolicy_RejectsInvalidTenantID(t *testing.T) {
	repo := newTPTestRepo()
	svc := service.NewTokenPolicyService(repo, nil)
	h := (&GRPCHandler{}).WithTokenPolicyService(svc)

	_, err := h.PutTokenPolicy(context.Background(), &authv1.PutTokenPolicyRequest{
		TenantId:   "not-a-uuid",
		ActorId:    uuid.NewString(),
		MaxTtlDays: wrapperspb.Int32(30),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestGRPCHandler_PutTokenPolicy_RejectsInvalidActorID(t *testing.T) {
	repo := newTPTestRepo()
	svc := service.NewTokenPolicyService(repo, nil)
	h := (&GRPCHandler{}).WithTokenPolicyService(svc)

	_, err := h.PutTokenPolicy(context.Background(), &authv1.PutTokenPolicyRequest{
		TenantId:   uuid.NewString(),
		ActorId:    "not-a-uuid",
		MaxTtlDays: wrapperspb.Int32(30),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}

func TestGRPCHandler_PutTokenPolicy_PropagatesServiceValidation(t *testing.T) {
	repo := newTPTestRepo()
	svc := service.NewTokenPolicyService(repo, nil)
	h := (&GRPCHandler{}).WithTokenPolicyService(svc)

	// idle_revoke_days = 3 → below the 7-day floor.
	_, err := h.PutTokenPolicy(context.Background(), &authv1.PutTokenPolicyRequest{
		TenantId:       uuid.NewString(),
		ActorId:        uuid.NewString(),
		IdleRevokeDays: wrapperspb.Int32(3),
	})
	require.Error(t, err)
	s, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, s.Code())
}
