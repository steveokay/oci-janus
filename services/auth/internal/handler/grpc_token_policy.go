// Package handler — grpc_token_policy.go is the gRPC layer for the
// FUT-003 workspace token policy feature. Two admin RPCs map to
// TokenPolicyService:
//
//	GetTokenPolicy — return the current policy or an empty (all-nil) row.
//	PutTokenPolicy — validate + persist; emit auth.token_policy.changed.
//
// Per the pattern established by grpc_oidc_trust.go, the gRPC layer
// trusts its caller — RBAC gates land in services/management's BFF.
// A future work item may thread the caller principal through gRPC
// metadata; for now the BFF passes actor_id via the wire field.
package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// errTokenPolicyNotConfigured is returned when a token-policy RPC is
// invoked but the service was constructed without a TokenPolicyService.
// Clear codes.Unimplemented so callers learn the feature is off.
var errTokenPolicyNotConfigured = status.Error(codes.Unimplemented,
	"token policy feature is not wired (WithTokenPolicyService not called at startup)")

// GetTokenPolicy returns the current policy for the tenant. Empty rows
// (no policy configured) return an all-nil TokenPolicy (not NotFound) so
// the caller can render "policy disabled" without a special case.
func (h *GRPCHandler) GetTokenPolicy(ctx context.Context, req *authv1.GetTokenPolicyRequest) (*authv1.TokenPolicy, error) {
	if h.tokenPolicy == nil {
		return nil, errTokenPolicyNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	row, err := h.tokenPolicy.Get(ctx, tenantID)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, status.Error(codes.Internal, "get token policy failed")
	}
	return tokenPolicyToProto(row), nil
}

// PutTokenPolicy validates the input + persists the update. Rejects with
// codes.InvalidArgument on validation failures; emits an audit event on
// success.
func (h *GRPCHandler) PutTokenPolicy(ctx context.Context, req *authv1.PutTokenPolicyRequest) (*authv1.TokenPolicy, error) {
	if h.tokenPolicy == nil {
		return nil, errTokenPolicyNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	actorID, err := uuid.Parse(req.GetActorId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid actor_id")
	}
	row, err := h.tokenPolicy.Put(ctx, service.PutTokenPolicyInput{
		TenantID:             tenantID,
		MaxTTLDays:           unwrapInt32(req.GetMaxTtlDays()),
		RotationIntervalDays: unwrapInt32(req.GetRotationIntervalDays()),
		IdleRevokeDays:       unwrapInt32(req.GetIdleRevokeDays()),
		ActorID:              actorID,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, status.Error(codes.Internal, "put token policy failed")
	}
	return tokenPolicyToProto(row), nil
}

// unwrapInt32 converts a proto Int32Value wrapper to a *int32 for the
// service layer. Nil wrapper → nil pointer (semantic "not set").
func unwrapInt32(w *wrapperspb.Int32Value) *int32 {
	if w == nil {
		return nil
	}
	v := w.GetValue()
	return &v
}

// wrapInt32 converts an in-memory *int32 to the proto Int32Value wrapper.
// Nil pointer → nil wrapper so the client renders "unset" distinctly
// from "zero".
func wrapInt32(v *int32) *wrapperspb.Int32Value {
	if v == nil {
		return nil
	}
	return wrapperspb.Int32(*v)
}

// tokenPolicyToProto converts a repository row to its proto representation.
// updated_by_user_id nullable → empty string when nil.
func tokenPolicyToProto(row *repository.TokenPolicy) *authv1.TokenPolicy {
	updatedBy := ""
	if row.UpdatedByUserID != nil {
		updatedBy = row.UpdatedByUserID.String()
	}
	return &authv1.TokenPolicy{
		TenantId:             row.TenantID.String(),
		MaxTtlDays:           wrapInt32(row.MaxTTLDays),
		RotationIntervalDays: wrapInt32(row.RotationIntervalDays),
		IdleRevokeDays:       wrapInt32(row.IdleRevokeDays),
		UpdatedAt:            timestamppb.New(row.UpdatedAt),
		UpdatedByUserId:      updatedBy,
	}
}
