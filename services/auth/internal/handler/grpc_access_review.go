// Package handler — grpc_access_review.go is the gRPC layer for the
// FUT-004 access-review feature. Two RPCs map to AccessReviewService:
//
//   - ListStaleKeys       — return the tenant's stale keys with the
//     suggested-action heuristic pre-applied.
//   - SnoozeAPIKeyReview  — validate + persist a per-key snooze; emits
//     auth.access_review.snoozed on success.
//
// Per the pattern established by grpc_token_policy.go, the gRPC layer
// trusts its caller — RBAC / owner-vs-admin gates land in
// services/management's BFF. The BFF plumbs actor_id through the wire
// field so the audit trail records the human who deferred the review.
package handler

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// errAccessReviewNotConfigured is returned when an access-review RPC is
// invoked but the service was constructed without an AccessReviewService.
// codes.Unimplemented is the clearest signal to the caller that the
// feature is off (vs a generic 5xx from a nil dereference).
var errAccessReviewNotConfigured = status.Error(codes.Unimplemented,
	"access review feature is not wired (WithAccessReviewService not called at startup)")

// ListStaleKeys returns the tenant's stale keys with the suggested-action
// heuristic applied per row. The service resolves the tenant's
// idle_revoke_days from token_policies (default 90 when unset), so the
// caller doesn't need to supply a threshold.
func (h *GRPCHandler) ListStaleKeys(ctx context.Context, req *authv1.ListStaleKeysRequest) (*authv1.ListStaleKeysResponse, error) {
	if h.accessReview == nil {
		return nil, errAccessReviewNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	views, err := h.accessReview.ListStaleKeys(ctx, tenantID)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, status.Error(codes.Internal, "list stale keys failed")
	}
	out := make([]*authv1.StaleKey, 0, len(views))
	for _, v := range views {
		out = append(out, staleKeyViewToProto(v))
	}
	return &authv1.ListStaleKeysResponse{Keys: out}, nil
}

// SnoozeAPIKeyReview defers the next weekly nudge for one key. Days is
// validated at both bounds by the service layer ([1, 90]) so an
// out-of-range value returns codes.InvalidArgument regardless of caller.
//
// tenant_id (SEC-069) is optional on the wire for rolling-deploy
// compatibility, but when present it must be a valid UUID and must match
// the key's own tenant (the service layer returns NotFound on mismatch).
func (h *GRPCHandler) SnoozeAPIKeyReview(ctx context.Context, req *authv1.SnoozeAPIKeyReviewRequest) (*authv1.StaleKey, error) {
	if h.accessReview == nil {
		return nil, errAccessReviewNotConfigured
	}
	keyID, err := uuid.Parse(req.GetKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid key_id")
	}
	actorID, err := uuid.Parse(req.GetActorId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid actor_id")
	}
	// Empty → uuid.Nil → the service skips the cross-check. A present-
	// but-garbled value is a caller bug and gets InvalidArgument rather
	// than being silently treated as "no tenant asserted".
	tenantID := uuid.Nil
	if raw := req.GetTenantId(); raw != "" {
		tenantID, err = uuid.Parse(raw)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
		}
	}
	got, err := h.accessReview.SnoozeAPIKeyReview(ctx, service.SnoozeAPIKeyReviewInput{
		KeyID:    keyID,
		Days:     req.GetDays(),
		ActorID:  actorID,
		TenantID: tenantID,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, status.Error(codes.Internal, "snooze api key review failed")
	}
	// Return the minimal StaleKey shape the service handed back — the FE
	// uses it to rehydrate the row's snooze badge without a list refetch.
	return staleKeyToProto(got, service.SuggestedActionUnspecified, ""), nil
}

// staleKeyViewToProto converts the service-layer view (row + heuristic
// output) into the proto message. The heuristic's SuggestedAction +
// Reason are the added fields — the rest maps 1:1 from the repo row.
func staleKeyViewToProto(v service.StaleKeyView) *authv1.StaleKey {
	return staleKeyToProto(&v.Key, v.SuggestedAction, v.Reason)
}

// staleKeyToProto converts a repository.StaleKey (plus heuristic output
// when available) into the proto message. Nil timestamps map to nil
// wrappers so the FE renders "never" distinctly from "1970-01-01".
func staleKeyToProto(k *repository.StaleKey, action service.SuggestedAction, reason string) *authv1.StaleKey {
	if k == nil {
		return nil
	}
	return &authv1.StaleKey{
		Id:                 k.ID.String(),
		TenantId:           k.TenantID.String(),
		OwnerUserId:        k.OwnerUserID.String(),
		Name:               k.Name,
		LastUsedAt:         nilableTS(k.LastUsedAt),
		RotationDueAt:      nilableTS(k.RotationDueAt),
		ReviewSnoozedUntil: nilableTS(k.ReviewSnoozedUntil),
		SuggestedAction:    suggestedActionToProto(action),
		Reason:             reason,
	}
}

// nilableTS converts a *time.Time to a *timestamppb.Timestamp, preserving
// nil so the FE can distinguish "never" from a zero timestamp.
func nilableTS(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// suggestedActionToProto maps the service-layer enum to the proto enum.
// The two are kept as separate types so the service layer doesn't need
// to depend on the proto package directly.
func suggestedActionToProto(a service.SuggestedAction) authv1.SuggestedAction {
	switch a {
	case service.SuggestedActionRevoke:
		return authv1.SuggestedAction_SUGGESTED_ACTION_REVOKE
	case service.SuggestedActionKeep:
		return authv1.SuggestedAction_SUGGESTED_ACTION_KEEP
	case service.SuggestedActionSnooze:
		return authv1.SuggestedAction_SUGGESTED_ACTION_SNOOZE
	default:
		return authv1.SuggestedAction_SUGGESTED_ACTION_UNSPECIFIED
	}
}
