package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// FUT-019 Phase 2 — per-user notification preferences gRPC handlers.
//
// Trust posture: the gRPC server is mTLS-only. The BFF is the gate
// that maps the JWT subject onto user_id + tenant_id before calling
// here; this handler validates UUID shape only.

// GetUserNotificationPreferences returns every explicit preference row
// for the user. Categories the user hasn't touched are NOT returned —
// the caller (BFF) merges the response against the known-category list
// + defaults (bell on, email off, webhook off) so the wire stays
// narrow.
func (h *GRPCHandler) GetUserNotificationPreferences(
	ctx context.Context,
	req *auditv1.GetUserNotificationPreferencesRequest,
) (*auditv1.GetUserNotificationPreferencesResponse, error) {
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	prefs, err := h.repo.GetUserPreferences(ctx, userID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to load preferences")
	}
	out := make([]*auditv1.NotificationPreference, len(prefs))
	for i, p := range prefs {
		out[i] = &auditv1.NotificationPreference{
			Category:       p.Category,
			BellEnabled:    p.BellEnabled,
			EmailEnabled:   p.EmailEnabled,
			WebhookEnabled: p.WebhookEnabled,
		}
	}
	return &auditv1.GetUserNotificationPreferencesResponse{Preferences: out}, nil
}

// UpdateUserNotificationPreferences upserts every preference in the
// request. The caller (BFF) sends the FULL set of categories the user
// is configuring — partial updates are explicit per-category rows
// rather than a JSON patch shape. Returns the same set back so the FE
// can re-cache without a follow-up Get.
func (h *GRPCHandler) UpdateUserNotificationPreferences(
	ctx context.Context,
	req *auditv1.UpdateUserNotificationPreferencesRequest,
) (*auditv1.UpdateUserNotificationPreferencesResponse, error) {
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	for _, p := range req.GetPreferences() {
		if p.GetCategory() == "" {
			return nil, status.Error(codes.InvalidArgument, "preference category must not be empty")
		}
		if err := h.repo.UpsertUserPreference(ctx, repository.NotificationPreference{
			UserID:         userID,
			TenantID:       tenantID,
			Category:       p.GetCategory(),
			BellEnabled:    p.GetBellEnabled(),
			EmailEnabled:   p.GetEmailEnabled(),
			WebhookEnabled: p.GetWebhookEnabled(),
		}); err != nil {
			return nil, status.Error(codes.Internal, "failed to save preference")
		}
	}
	return &auditv1.UpdateUserNotificationPreferencesResponse{
		Preferences: req.GetPreferences(),
	}, nil
}
