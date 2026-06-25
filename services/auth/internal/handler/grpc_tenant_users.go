// Package handler — FUT-012 Phase A gRPC handlers.
//
// Three new RPCs that back the tenant-user lifecycle management UI:
//
//   ListTenantUsers  — paginated tenant member list with role summary.
//   InviteUser       — create users row in 'invited' status, return
//                      the raw single-use token ONCE.
//   SetUserDisabled  — flip status between 'active' and 'disabled',
//                      revoke active JWTs + disable API keys on the
//                      disable path.
//
// Per the existing pattern (CountTenantUsers / ListMembers), the
// gRPC layer trusts its caller — RBAC gates land in services/management's
// BFF (Phase B). The mTLS server credential check enforces that the
// caller is a registered platform service, and the BFF's
// hasScopedRole("tenant", <tenant_id>, "admin") || hasPlatformAdmin
// check gates user-facing access.
package handler

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ListTenantUsers (FUT-012 Phase A).
func (h *GRPCHandler) ListTenantUsers(ctx context.Context, req *authv1.ListTenantUsersRequest) (*authv1.ListTenantUsersResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	users, nextToken, total, err := h.svc.ListTenantUsers(ctx, tenantID, repository.ListTenantUsersOpts{
		PageSize:  req.GetPageSize(),
		PageToken: req.GetPageToken(),
	})
	if err != nil {
		return nil, errcodes.MapDBError(err, "list tenant users")
	}

	out := make([]*authv1.TenantUser, len(users))
	for i, u := range users {
		var lastLogin *timestamppb.Timestamp
		if u.LastLoginAt != nil {
			lastLogin = timestamppb.New(*u.LastLoginAt)
		}
		out[i] = &authv1.TenantUser{
			UserId:      u.UserID.String(),
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Email:       u.Email,
			Kind:        u.Kind,
			Status:      u.Status,
			LastLoginAt: lastLogin,
			CreatedAt:   timestamppb.New(u.CreatedAt),
			Roles: &authv1.RoleSummary{
				OrgAdminCount:  u.OrgAdminCount,
				OrgWriterCount: u.OrgWriterCount,
				OrgReaderCount: u.OrgReaderCount,
				RepoGrantCount: u.RepoGrantCount,
				TenantAdmin:    u.TenantAdmin,
				PlatformAdmin:  u.PlatformAdmin,
			},
		}
	}
	return &authv1.ListTenantUsersResponse{
		Users:         out,
		NextPageToken: nextToken,
		TotalCount:    total,
	}, nil
}

// InviteUser (FUT-012 Phase A). The raw invite_token in the response
// MUST be surfaced to the operator only once — the BFF + FE persist it
// in the immediate UX (copy-link button); after the modal closes the
// token is unrecoverable because the DB stores only the argon2id hash.
func (h *GRPCHandler) InviteUser(ctx context.Context, req *authv1.InviteUserRequest) (*authv1.InviteUserResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	invitedBy, err := uuid.Parse(req.GetInvitedBy())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid invited_by")
	}
	if req.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if req.GetDisplayName() == "" {
		return nil, status.Error(codes.InvalidArgument, "display_name is required")
	}
	// initial_org_role + initial_org_name are paired — either both
	// set or neither. Surface the inconsistency as a clean 400 instead
	// of silently dropping the half-supplied grant.
	if (req.GetInitialOrgRole() == "") != (req.GetInitialOrgName() == "") {
		return nil, status.Error(codes.InvalidArgument, "initial_org_role and initial_org_name must be set together")
	}

	result, err := h.svc.InviteUser(ctx, service.InviteUserInput{
		TenantID:       tenantID,
		Email:          req.GetEmail(),
		DisplayName:    req.GetDisplayName(),
		InvitedBy:      invitedBy,
		InitialOrgRole: req.GetInitialOrgRole(),
		InitialOrgName: req.GetInitialOrgName(),
		ExpiresIn:      time.Duration(req.GetExpiresInSecs()) * time.Second,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidEmail):
			return nil, status.Error(codes.InvalidArgument, "invalid email")
		case errors.Is(err, service.ErrInvalidDisplayName):
			return nil, status.Error(codes.InvalidArgument, "invalid display_name")
		case errors.Is(err, repository.ErrAlreadyExists):
			return nil, status.Error(codes.AlreadyExists, "user with that username or email already exists")
		}
		return nil, errcodes.MapDBError(err, "invite user")
	}

	return &authv1.InviteUserResponse{
		UserId:           result.UserID.String(),
		InviteToken:      result.InviteToken,
		InviteExpiresAt:  timestamppb.New(result.InviteExpiresAt),
	}, nil
}

// SetUserDisabled (FUT-012 Phase A).
func (h *GRPCHandler) SetUserDisabled(ctx context.Context, req *authv1.SetUserDisabledRequest) (*authv1.SetUserDisabledResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	resulting, err := h.svc.SetUserDisabled(ctx, tenantID, userID, req.GetDisabled())
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			return nil, status.Error(codes.NotFound, "user not found")
		case errors.Is(err, service.ErrInvalidStatusTransition):
			// Surface as FailedPrecondition so the BFF can render
			// "this user is still pending an invite; cancel the
			// invite first" instead of a generic 400.
			return nil, status.Error(codes.FailedPrecondition, "user is in invited state; cancel the invite instead")
		}
		return nil, errcodes.MapDBError(err, "set user disabled")
	}
	return &authv1.SetUserDisabledResponse{Status: resulting}, nil
}
