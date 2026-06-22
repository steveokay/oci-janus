// Package handler contains the gRPC and HTTP request handlers for registry-auth.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// GRPCHandler implements authv1.AuthServiceServer.
type GRPCHandler struct {
	authv1.UnimplementedAuthServiceServer
	svc *service.Service
	pub *publisher.Publisher // may be nil in test/dev environments without RabbitMQ
}

// NewGRPCHandler creates a GRPCHandler backed by the given service.
// pub may be nil; if nil, RBAC events are logged but not published (e.g. test environments).
func NewGRPCHandler(svc *service.Service, pub *publisher.Publisher) *GRPCHandler {
	return &GRPCHandler{svc: svc, pub: pub}
}

// ValidateToken parses the JWT, checks the revocation list, and returns the claims.
func (h *GRPCHandler) ValidateToken(ctx context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	claims, err := h.svc.ValidateToken(ctx, req.GetToken())
	if err != nil {
		if errors.Is(err, service.ErrTokenRevoked) {
			return nil, status.Error(codes.Unauthenticated, "token has been revoked")
		}
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	protoAccess := make([]*authv1.RepositoryAccess, len(claims.Access))
	for i, a := range claims.Access {
		protoAccess[i] = &authv1.RepositoryAccess{
			Type:    a.Type,
			Name:    a.Name,
			Actions: a.Actions,
		}
	}

	return &authv1.ValidateTokenResponse{
		Valid:     true,
		UserId:    claims.Subject,
		TenantId:  claims.TenantID,
		Jti:       claims.ID,
		Access:    protoAccess,
		ExpiresAt: timestamppb.New(claims.ExpiresAt.Time),
		Roles:     claims.Roles,
	}, nil
}

// ValidateAPIKey checks the key hash and returns the associated identity.
func (h *GRPCHandler) ValidateAPIKey(ctx context.Context, req *authv1.ValidateAPIKeyRequest) (*authv1.ValidateAPIKeyResponse, error) {
	keyID, err := uuid.Parse(req.GetKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid key_id")
	}

	key, err := h.svc.ValidateAPIKey(ctx, keyID, req.GetRawSecret())
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) || errors.Is(err, service.ErrKeyExpired) {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Resolve the owner identity for the gRPC response. For human-owned keys,
	// UserID is non-nil. SA-owned key JWT exchange ships in T9
	// (ServiceAccountService); until then, refuse explicitly rather than return
	// a ValidateAPIKeyResponse with an empty user_id that downstream services
	// would treat as unauthenticated noise.
	if key.UserID == nil {
		return nil, status.Error(codes.Unimplemented, "service-account key token exchange is not yet supported")
	}
	return &authv1.ValidateAPIKeyResponse{
		Valid:     true,
		UserId:    key.UserID.String(),
		TenantId:  key.TenantID.String(),
		Access:    scopesToProto(key.Scopes),
	}, nil
}

// GetUserPermissions returns the access scopes and roles for a user.
// It loads RBAC role assignments from the database and maps them to RepositoryAccess
// entries based on the role hierarchy: owner/admin → push+pull+delete, writer → push+pull, reader → pull.
func (h *GRPCHandler) GetUserPermissions(ctx context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	_, err = h.svc.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, errcodes.MapDBError(err, "internal error")
	}

	assignments, err := h.svc.GetUserRoles(ctx, userID, tenantID)
	if err != nil {
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Map each role assignment to a RepositoryAccess entry and to a full
	// RoleAssignment proto. The `access` list drives OCI push/pull authorization
	// in registry-core; the `role_assignments` list is what callers must use for
	// scope-aware authorization decisions (PENTEST-002) — `roles` alone loses
	// the scope and cannot prevent cross-scope privilege escalation.
	var protoAccess []*authv1.RepositoryAccess
	var roleNames []string
	var protoAssignments []*authv1.RoleAssignment

	for _, a := range assignments {
		actions := actionsForRole(a.RoleName)
		name := a.ScopeValue
		if a.ScopeType == "org" {
			// Grant access to all repos in the org via "org/*" pattern.
			name = a.ScopeValue + "/*"
		}
		protoAccess = append(protoAccess, &authv1.RepositoryAccess{
			Type:    "repository",
			Name:    name,
			Actions: actions,
		})
		roleNames = append(roleNames, a.RoleName)
		protoAssignments = append(protoAssignments, &authv1.RoleAssignment{
			Id:         a.ID.String(),
			UserId:     a.UserID.String(),
			Role:       a.RoleName,
			ScopeType:  a.ScopeType,
			ScopeValue: a.ScopeValue,
			GrantedBy:  a.GrantedBy.String(),
		})
	}

	if protoAccess == nil {
		protoAccess = []*authv1.RepositoryAccess{}
	}
	if roleNames == nil {
		roleNames = []string{}
	}
	if protoAssignments == nil {
		protoAssignments = []*authv1.RoleAssignment{}
	}

	return &authv1.GetUserPermissionsResponse{
		Access:          protoAccess,
		Roles:           roleNames,
		RoleAssignments: protoAssignments,
	}, nil
}

// actionsForRole returns the OCI action list for a given role name.
// Role hierarchy: reader < writer < admin < owner.
func actionsForRole(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"push", "pull", "delete"}
	case "writer":
		return []string{"push", "pull"}
	default: // "reader" and any unknown role
		return []string{"pull"}
	}
}

// GrantRole creates a role assignment for a user within a tenant scope.
// On success it publishes an rbac.role_granted event to RabbitMQ so that
// registry-audit can record the change without a direct gRPC coupling.
func (h *GRPCHandler) GrantRole(ctx context.Context, req *authv1.GrantRoleRequest) (*emptypb.Empty, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	// granted_by is optional; zero UUID is acceptable.
	grantedBy := uuid.Nil
	if gb := req.GetGrantedBy(); gb != "" {
		if grantedBy, err = uuid.Parse(gb); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid granted_by")
		}
	}

	if req.GetRole() == "" {
		return nil, status.Error(codes.InvalidArgument, "role must not be empty")
	}
	if req.GetScopeType() == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_type must not be empty")
	}
	if req.GetScopeValue() == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_value must not be empty")
	}

	err = h.svc.GrantRole(ctx, repository.RoleAssignment{
		TenantID:   tenantID,
		UserID:     userID,
		RoleName:   req.GetRole(),
		ScopeType:  req.GetScopeType(),
		ScopeValue: req.GetScopeValue(),
		GrantedBy:  grantedBy,
	})
	if err != nil {
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Publish audit event after the DB write succeeds. A publish failure is logged
	// but does not fail the RPC — the grant is already committed to the DB and will
	// appear in any direct query; the audit trail gap is acceptable vs. rollback complexity.
	h.publishRoleGranted(ctx, req.GetTenantId(), req.GetUserId(), req.GetRole(),
		req.GetScopeType(), req.GetScopeValue(), grantedBy.String())

	return &emptypb.Empty{}, nil
}

// publishRoleGranted emits an rbac.role_granted event. Errors are logged, not returned,
// so a broker outage never blocks the RBAC operation that already succeeded in the DB.
func (h *GRPCHandler) publishRoleGranted(ctx context.Context, tenantID, userID, role, scopeType, scopeValue, grantedBy string) {
	if h.pub == nil {
		return
	}
	payload, err := json.Marshal(events.RoleGrantedPayload{
		TenantID:   tenantID,
		UserID:     userID,
		Role:       role,
		ScopeType:  scopeType,
		ScopeValue: scopeValue,
		GrantedBy:  grantedBy,
	})
	if err != nil {
		slog.Error("marshal rbac.role_granted payload", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingRBACRoleGranted,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(ctx, events.RoutingRBACRoleGranted, evt); err != nil {
		slog.Error("publish rbac.role_granted", "err", err, "tenant_id", tenantID, "user_id", userID)
	}
}

// RevokeRole deletes the role assignment with the given ID within the tenant.
// On success it publishes an rbac.role_revoked event to RabbitMQ.
//
// PENTEST-011: when the caller passes expected_scope_type and expected_scope_value,
// the deletion only proceeds if the assignment actually matches that scope. This
// defends against scope-confusion where an admin-of-org-A submits an assignment ID
// belonging to org-B and the URL path "/orgs/org-A/members/{id}".
func (h *GRPCHandler) RevokeRole(ctx context.Context, req *authv1.RevokeRoleRequest) (*emptypb.Empty, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	assignmentID, err := uuid.Parse(req.GetAssignmentId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid assignment_id")
	}

	// revokedBy is extracted from gRPC metadata if present; for now we default
	// to the zero UUID (system) since the current RevokeRoleRequest has no actor field.
	revokedBy := uuid.Nil.String()

	err = h.svc.RevokeRoleScoped(ctx, assignmentID, tenantID, req.GetExpectedScopeType(), req.GetExpectedScopeValue())
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Return NotFound regardless of whether the row didn't exist or the
			// scope mismatched — don't leak which condition failed.
			return nil, status.Error(codes.NotFound, "assignment not found")
		}
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Publish audit event after the DB delete succeeds.
	h.publishRoleRevoked(ctx, req.GetTenantId(), req.GetAssignmentId(), revokedBy)

	return &emptypb.Empty{}, nil
}

// publishRoleRevoked emits an rbac.role_revoked event. Errors are logged, not returned.
func (h *GRPCHandler) publishRoleRevoked(ctx context.Context, tenantID, assignmentID, revokedBy string) {
	if h.pub == nil {
		return
	}
	payload, err := json.Marshal(events.RoleRevokedPayload{
		TenantID:     tenantID,
		AssignmentID: assignmentID,
		RevokedBy:    revokedBy,
	})
	if err != nil {
		slog.Error("marshal rbac.role_revoked payload", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingRBACRoleRevoked,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(ctx, events.RoutingRBACRoleRevoked, evt); err != nil {
		slog.Error("publish rbac.role_revoked", "err", err, "tenant_id", tenantID, "assignment_id", assignmentID)
	}
}

// ListMembers returns all role assignments within a tenant scope, enriched with
// the principal kind and display name so the dashboard can render human users
// and service accounts differently without a second round-trip.
//
// The proto RoleAssignment.user_id carries the users.id for all principal kinds
// (for service accounts this is the shadow_user_id). The proto Id field is left
// empty because the new Member view omits the assignment primary key; callers
// that need the assignment id for revocation must use GetUserPermissions.
func (h *GRPCHandler) ListMembers(ctx context.Context, req *authv1.ListMembersRequest) (*authv1.ListMembersResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if req.GetScopeType() == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_type must not be empty")
	}
	if req.GetScopeValue() == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_value must not be empty")
	}

	members, err := h.svc.ListMembers(ctx, tenantID, req.GetScopeType(), req.GetScopeValue())
	if err != nil {
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Map repository.Member to the proto RoleAssignment. The scope fields are
	// not stored on Member (they are the same for every row in the result set)
	// so they are copied from the request.
	out := make([]*authv1.RoleAssignment, len(members))
	for i, m := range members {
		out[i] = &authv1.RoleAssignment{
			UserId:     m.UserID.String(),
			Role:       m.Role,
			ScopeType:  req.GetScopeType(),
			ScopeValue: req.GetScopeValue(),
			GrantedBy:  m.GrantedBy.String(),
		}
	}
	return &authv1.ListMembersResponse{Members: out}, nil
}

// CountTenantUsers returns the number of users in the tenant (FE-API-028).
// Used by registry-management to populate the admin tenant-detail card.
// Errors from the underlying DB query map to Internal via MapDBError so the
// management layer can log + return a generic 500 without leaking driver
// detail to the caller.
func (h *GRPCHandler) CountTenantUsers(ctx context.Context, req *authv1.CountTenantUsersRequest) (*authv1.CountTenantUsersResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	n, err := h.svc.CountTenantUsers(ctx, tenantID)
	if err != nil {
		return nil, errcodes.MapDBError(err, "count tenant users")
	}
	return &authv1.CountTenantUsersResponse{Count: n}, nil
}

// scopesToProto wraps a flat scope list as a single wildcard RepositoryAccess.
// This is a Sprint 1 simplification; full scope-to-access mapping comes later.
func scopesToProto(scopes []string) []*authv1.RepositoryAccess {
	if len(scopes) == 0 {
		return nil
	}
	return []*authv1.RepositoryAccess{{
		Type:    "repository",
		Name:    "*",
		Actions: scopes,
	}}
}
