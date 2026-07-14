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

	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// GRPCHandler implements authv1.AuthServiceServer.
type GRPCHandler struct {
	authv1.UnimplementedAuthServiceServer
	svc *service.Service
	pub *publisher.Publisher // may be nil in test/dev environments without RabbitMQ
	// oidc is the FUT-001 trust + workload-token-exchange service. May be
	// nil when OIDC_ALLOWED_ISSUERS is unset; the 5 OIDC RPCs return
	// codes.Unimplemented in that case so callers learn the feature is
	// off rather than seeing a generic 5xx.
	oidc *service.OIDCTrustService
	// tokenPolicy is the FUT-003 workspace token policy service. Wired via
	// WithTokenPolicyService at startup. May be nil in test/dev fixtures;
	// the 2 policy RPCs return codes.Unimplemented in that case.
	tokenPolicy *service.TokenPolicyService
	// accessReview is the FUT-004 access-review service. Wired via
	// WithAccessReviewService at startup. May be nil in test/dev
	// fixtures; the 2 review RPCs return codes.Unimplemented in that case.
	accessReview *service.AccessReviewService
	// saService is the FUT-082 service-account service. Wired via
	// WithServiceAccountService at startup so ListServiceAccounts is served.
	// Shares the same instance the HTTP handler uses. May be nil when the SA
	// feature is not wired (mirrors the HTTP handler's requireSAService); the
	// ListServiceAccounts RPC returns codes.Unimplemented in that case so
	// callers learn the feature is off rather than seeing a nil-deref panic.
	saService *service.ServiceAccountService
}

// NewGRPCHandler creates a GRPCHandler backed by the given service.
// pub may be nil; if nil, RBAC events are logged but not published (e.g. test environments).
func NewGRPCHandler(svc *service.Service, pub *publisher.Publisher) *GRPCHandler {
	return &GRPCHandler{svc: svc, pub: pub}
}

// WithOIDCTrustService wires the FUT-001 service so the 5 OIDC RPCs are
// served. Returns the receiver so the call chains cleanly off
// NewGRPCHandler. Pass nil at construction time to indicate the feature
// is off (the OIDC RPCs return Unimplemented).
func (h *GRPCHandler) WithOIDCTrustService(oidc *service.OIDCTrustService) *GRPCHandler {
	h.oidc = oidc
	return h
}

// WithTokenPolicyService wires the FUT-003 service so Get/PutTokenPolicy
// RPCs are served. Chained builder like WithOIDCTrustService — pass nil
// to indicate the feature is off (RPCs return Unimplemented).
func (h *GRPCHandler) WithTokenPolicyService(tp *service.TokenPolicyService) *GRPCHandler {
	h.tokenPolicy = tp
	return h
}

// WithAccessReviewService wires the FUT-004 service so ListStaleKeys +
// SnoozeAPIKeyReview RPCs are served. Chained builder — pass nil to
// indicate the feature is off (RPCs return Unimplemented).
func (h *GRPCHandler) WithAccessReviewService(ar *service.AccessReviewService) *GRPCHandler {
	h.accessReview = ar
	return h
}

// WithServiceAccountService wires the FUT-082 service-account service so the
// ListServiceAccounts RPC is served. Chained builder like the others — pass
// nil to indicate the feature is off (the RPC returns Unimplemented). Server
// wiring passes the same *service.ServiceAccountService instance the HTTP
// handler already uses so both surfaces share one repo/audit/cache path.
func (h *GRPCHandler) WithServiceAccountService(sa *service.ServiceAccountService) *GRPCHandler {
	h.saService = sa
	return h
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
// As of T9, both human-owned and service-account-owned keys are supported.
// The request_tenant_id for the cross-tenant guard (spec §5.4) is threaded
// through gRPC metadata by the gateway interceptor (T13); for now the proto
// field is not yet wired so RequestTenantID is always nil on this path.
func (h *GRPCHandler) ValidateAPIKey(ctx context.Context, req *authv1.ValidateAPIKeyRequest) (*authv1.ValidateAPIKeyResponse, error) {
	keyID, err := uuid.Parse(req.GetKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid key_id")
	}

	vk, err := h.svc.ValidateAPIKey(ctx, service.ValidateAPIKeyOpts{
		KeyID:     keyID,
		RawSecret: req.GetRawSecret(),
		// RequestTenantID will be wired from gRPC metadata in T13.
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) ||
			errors.Is(err, service.ErrKeyExpired) ||
			errors.Is(err, service.ErrAccountDisabled) {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return nil, errcodes.MapDBError(err, "internal error")
	}

	return &authv1.ValidateAPIKeyResponse{
		Valid:    true,
		UserId:   vk.UserID.String(),
		TenantId: vk.TenantID.String(),
		Access:   scopesToProto(vk.EffectiveScopes),
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

	user, err := h.svc.GetUserByID(ctx, userID)
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
		// IsGlobalAdmin reflects users.is_global_admin (REDESIGN-001 Phase 5.1).
		// The typed column replaces the (admin, org, '*') legacy marker so
		// callers no longer need to inspect role_assignments for the magic scope.
		IsGlobalAdmin: user.IsGlobalAdmin,
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

	// Forbid the deprecated platform-admin marker. Use SetGlobalAdmin instead.
	// REDESIGN-001 Phase 5.1: the (admin, org, '*') convention was a string
	// privilege that any GrantRole caller could mint by passing scope_value='*'.
	// The typed users.is_global_admin column removes that footgun.
	if req.GetScopeType() == "org" && req.GetScopeValue() == "*" {
		return nil, status.Error(codes.InvalidArgument,
			"scope_value '*' is no longer a valid platform-admin marker; use SetGlobalAdmin instead (REDESIGN-001 Phase 5.1)")
	}

	// REDESIGN-001 Phase 5.3 — enforce delegator dominates delegatee.
	// A non-nil granted_by means a human (or service principal) is the actor
	// behind this grant; we load their role assignments in the tenant and
	// require at least one assignment whose scope dominates the target AND
	// whose rank is >= the rank of the role being granted.
	//
	// granted_by == uuid.Nil is reserved for system/bootstrap grants (initial
	// seed, the bootstrap CLI). Those callers bypass the check on purpose —
	// they predate any role_assignments rows that could authorise them.
	// Global admins (users.is_global_admin) also bypass: they are the
	// platform's effective "root" and can grant any role at any scope.
	if grantedBy != uuid.Nil {
		actor, gErr := h.svc.GetUserByID(ctx, grantedBy)
		if gErr != nil && !errors.Is(gErr, repository.ErrNotFound) {
			return nil, errcodes.MapDBError(gErr, "internal error")
		}
		// A global admin short-circuits the delegation check. We still
		// require the actor row to exist when granted_by is non-nil — an
		// unknown actor cannot delegate (NotFound from GetUserByID would
		// have been swallowed above by the errors.Is guard, so on the
		// ErrNotFound branch `actor` is nil and we fall through to the
		// dominance check, which will deny).
		if actor == nil || !actor.IsGlobalAdmin {
			callerAssignments, aErr := h.svc.GetUserRoles(ctx, grantedBy, tenantID)
			if aErr != nil {
				return nil, errcodes.MapDBError(aErr, "internal error")
			}
			if dErr := service.VerifyDelegationBound(
				callerAssignments,
				req.GetRole(),
				req.GetScopeType(),
				req.GetScopeValue(),
			); dErr != nil {
				return nil, dErr
			}
		}
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
// (for service accounts this is the shadow_user_id). The proto Id field is set
// from Member.AssignmentID (role_assignments.id) so the frontend revoke flow
// (useRevokeOrgRole / useRevokeRepoRole DELETE /orgs/{org}/members/{assignmentId})
// has the correct assignment primary key to send.
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
	// so they are copied from the request. Id is set from AssignmentID
	// (role_assignments.id) so the frontend revoke flow receives the correct
	// assignment primary key.
	out := make([]*authv1.RoleAssignment, len(members))
	for i, m := range members {
		out[i] = &authv1.RoleAssignment{
			Id:                   m.AssignmentID.String(),
			UserId:               m.UserID.String(),
			Role:                 m.Role,
			ScopeType:            req.GetScopeType(),
			ScopeValue:           req.GetScopeValue(),
			GrantedBy:            m.GrantedBy.String(),
			Username:             m.Username,
			DisplayName:          m.DisplayName,
			GrantedByUsername:    m.GrantedByUsername,
			GrantedByDisplayName: m.GrantedByDisplayName,
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

// lookupUsernamesMaxBatch caps the batch size so a misbehaving BFF can't
// fan out a multi-thousand-id lookup. 200 mirrors the audit / notification
// page cap which is the main upstream call site.
const lookupUsernamesMaxBatch = 200

// LookupUsernames (REM-018-followup) batch-resolves user_ids to
// (username, display_name) tuples within a tenant. Used by
// services/management to enrich `/api/v1/notifications` + activity-feed
// responses so the FE renders a friendly label instead of a raw UUID.
//
// Validation:
//   - tenant_id MUST be a UUID (cross-tenant isolation enforced in SQL).
//   - empty user_ids → empty response (not an error; lets the BFF skip
//     the round-trip without branching).
//   - >lookupUsernamesMaxBatch ids → InvalidArgument.
//   - Malformed ids are dropped silently (the BFF passes through actor_id
//     values from audit which may include the literal "system" or
//     "anonymous" sentinels — these are not UUIDs).
//
// Unknown / cross-tenant ids are dropped from the result. Caller iterates
// by its input set and renders the UUID / system fallback for absent ids.
//
//nolint:dupl // parallel batch-RPC handler; ResolveUserEmails deliberately mirrors this parse/dedupe/cap shape.
func (h *GRPCHandler) LookupUsernames(ctx context.Context, req *authv1.LookupUsernamesRequest) (*authv1.LookupUsernamesResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	raw := req.GetUserIds()
	if len(raw) == 0 {
		return &authv1.LookupUsernamesResponse{}, nil
	}
	if len(raw) > lookupUsernamesMaxBatch {
		return nil, status.Errorf(codes.InvalidArgument,
			"user_ids exceeds batch cap of %d", lookupUsernamesMaxBatch)
	}
	// Dedupe + parse. Drop non-UUID values silently so the BFF can pass
	// the raw audit actor_id list through without filtering "system" /
	// "anonymous" sentinel strings itself.
	seen := make(map[uuid.UUID]struct{}, len(raw))
	parsed := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, perr := uuid.Parse(s)
		if perr != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parsed = append(parsed, id)
	}
	if len(parsed) == 0 {
		return &authv1.LookupUsernamesResponse{}, nil
	}
	summaries, err := h.svc.LookupUsernames(ctx, tenantID, parsed)
	if err != nil {
		return nil, errcodes.MapDBError(err, "lookup usernames")
	}
	out := make([]*authv1.UserLookupResult, len(summaries))
	for i, s := range summaries {
		out[i] = &authv1.UserLookupResult{
			UserId:      s.ID.String(),
			Username:    s.Username,
			DisplayName: s.DisplayName,
		}
	}
	return &authv1.LookupUsernamesResponse{Users: out}, nil
}

// ResolveUserEmails (FUT-019 Phase 3) batch-resolves a set of user_ids to their
// email addresses within a tenant. Used by registry-audit to resolve
// email-notification recipients. Near-clone of LookupUsernames: same tenant
// parse, empty short-circuit, per-request batch cap, and non-UUID dedupe.
// Users with no email are dropped by the repo, so the response may be shorter
// than the request set. email_verified is informational only (currently always
// false — the users table has no verification column) and never gates delivery.
//
//nolint:dupl // intentional parallel batch-RPC handler; deliberately mirrors LookupUsernames (same parse/dedupe/cap shape), not worth a shared helper that couples two RPCs.
func (h *GRPCHandler) ResolveUserEmails(ctx context.Context, req *authv1.ResolveUserEmailsRequest) (*authv1.ResolveUserEmailsResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	raw := req.GetUserIds()
	if len(raw) == 0 {
		return &authv1.ResolveUserEmailsResponse{}, nil
	}
	// Reuse the LookupUsernames batch cap — same upstream call shape.
	if len(raw) > lookupUsernamesMaxBatch {
		return nil, status.Errorf(codes.InvalidArgument,
			"user_ids exceeds batch cap of %d", lookupUsernamesMaxBatch)
	}
	// Dedupe + parse. Drop non-UUID values silently so the caller can pass a
	// raw recipient id list through without pre-filtering sentinel strings.
	seen := make(map[uuid.UUID]struct{}, len(raw))
	parsed := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, perr := uuid.Parse(s)
		if perr != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parsed = append(parsed, id)
	}
	if len(parsed) == 0 {
		return &authv1.ResolveUserEmailsResponse{}, nil
	}
	emails, err := h.svc.ResolveUserEmails(ctx, tenantID, parsed)
	if err != nil {
		return nil, errcodes.MapDBError(err, "resolve user emails")
	}
	out := make([]*authv1.ResolvedEmail, len(emails))
	for i, e := range emails {
		out[i] = &authv1.ResolvedEmail{
			UserId:        e.ID.String(),
			Email:         e.Email,
			EmailVerified: e.EmailVerified,
		}
	}
	return &authv1.ResolveUserEmailsResponse{Emails: out}, nil
}

// SetGlobalAdmin sets or clears users.is_global_admin for the given user.
// Only callers that are themselves global admins may invoke this (enforced
// in the management BFF); the bootstrap CLI writes the flag directly via SQL
// on first run and does not call this RPC.
//
// On success an rbac.role_granted / rbac.role_revoked event is emitted with
// the synthetic role name "global_admin" so the audit catalogue surfaces the
// change in /activity. REDESIGN-001 Phase 5.1.
func (h *GRPCHandler) SetGlobalAdmin(ctx context.Context, req *authv1.SetGlobalAdminRequest) (*emptypb.Empty, error) {
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	// actorID is optional — the zero UUID is acceptable for system-initiated changes.
	actorID := uuid.Nil
	if a := req.GetActorId(); a != "" {
		if actorID, err = uuid.Parse(a); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid actor_id")
		}
	}

	if err := h.svc.SetGlobalAdmin(ctx, userID, req.GetGranted(), actorID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, errcodes.MapDBError(err, "internal error")
	}

	// Publish audit event after the DB write. A publish failure is logged but
	// does not fail the RPC — the flag is already set in the DB and will be
	// picked up by any subsequent GetUserPermissions call.
	h.publishGlobalAdminChanged(ctx, req.GetUserId(), req.GetGranted(), actorID.String())

	return &emptypb.Empty{}, nil
}

// publishGlobalAdminChanged emits an rbac.role_granted / rbac.role_revoked event
// for a global-admin flag change. Both grant and revoke use the RoleGrantedPayload
// shape (which carries the full grant context); the routing key differentiates the
// direction. Errors are logged, not returned, so a broker outage never blocks the
// operation that already succeeded in the DB.
func (h *GRPCHandler) publishGlobalAdminChanged(ctx context.Context, userID string, granted bool, actorID string) {
	if h.pub == nil {
		return
	}
	routingKey := events.RoutingRBACRoleGranted
	if !granted {
		routingKey = events.RoutingRBACRoleRevoked
	}
	// Use RoleGrantedPayload for both directions: it carries the actor context
	// (GrantedBy) that the audit catalogue needs to render the /activity entry
	// regardless of whether the change was a grant or revoke.
	payload, err := json.Marshal(events.RoleGrantedPayload{
		UserID:    userID,
		Role:      "global_admin",
		ScopeType: "global",
		GrantedBy: actorID,
	})
	if err != nil {
		slog.Error("marshal global_admin audit payload", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       routingKey,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(ctx, routingKey, evt); err != nil {
		slog.Error("publish global_admin change", "err", err, "user_id", userID, "granted", granted)
	}
}

// Default and maximum page sizes for ListServiceAccounts. A request with
// page_size == 0 falls back to listServiceAccountsDefaultPageSize; anything
// larger than listServiceAccountsMaxPageSize is capped so a caller cannot
// force an unbounded scan.
const (
	listServiceAccountsDefaultPageSize = 50
	listServiceAccountsMaxPageSize     = 200
)

// ListServiceAccounts returns a page of the tenant's service accounts as
// ServiceAccountSummary rows (FUT-082). It is the gRPC surface over the same
// ServiceAccountService.List the HTTP handler uses, so registry-management can
// render the SA admin list via a single BFF→gRPC hop.
//
// Behaviour:
//   - saService not wired → codes.Unimplemented (mirrors the HTTP handler's
//     requireSAService 501 posture; the feature is simply off).
//   - tenant_id not a UUID → codes.InvalidArgument.
//   - page_size: 0 defaults to 50, values above 200 are capped at 200.
//
// The repository's ServiceAccountWithStats rows are mapped to the proto
// summary: Disabled is derived from DisabledAt (non-nil ⇒ disabled) and
// LastUsedAt is emitted only when a key has actually been used (nil otherwise).
func (h *GRPCHandler) ListServiceAccounts(ctx context.Context, req *authv1.ListServiceAccountsRequest) (*authv1.ListServiceAccountsResponse, error) {
	// The SA feature is optional: when the service is not wired the RPC is
	// unavailable. Return Unimplemented (not a panic) so callers can degrade.
	if h.saService == nil {
		return nil, status.Error(codes.Unimplemented, "service accounts are not enabled")
	}

	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Normalise page_size: default when unset, cap at the max to bound the scan.
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = listServiceAccountsDefaultPageSize
	}
	if pageSize > listServiceAccountsMaxPageSize {
		pageSize = listServiceAccountsMaxPageSize
	}

	rows, nextToken, err := h.saService.List(ctx, tenantID, req.GetIncludeDisabled(), pageSize, req.GetPageToken())
	if err != nil {
		return nil, errcodes.MapDBError(err, "list service accounts")
	}

	// Map each repository row to a proto summary. Disabled is a derived flag
	// (DisabledAt non-nil); LastUsedAt is only populated when a key has been
	// used so the FE can distinguish "never used" from "used at epoch".
	summaries := make([]*authv1.ServiceAccountSummary, len(rows))
	for i, r := range rows {
		s := &authv1.ServiceAccountSummary{
			Id:             r.ID.String(),
			TenantId:       r.TenantID.String(),
			Name:           r.Name,
			Description:    r.Description,
			AllowedScopes:  r.AllowedScopes,
			Disabled:       r.DisabledAt != nil,
			ActiveKeyCount: r.ActiveKeyCount,
			CreatedAt:      timestamppb.New(r.CreatedAt),
		}
		if r.LastUsedAt != nil {
			s.LastUsedAt = timestamppb.New(*r.LastUsedAt)
		}
		summaries[i] = s
	}

	return &authv1.ListServiceAccountsResponse{
		ServiceAccounts: summaries,
		NextPageToken:   nextToken,
	}, nil
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
