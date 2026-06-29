// Package handler — tenant_users.go
//
// FUT-012 Phase B — REST routes wrapping the three new AuthService
// RPCs (Phase A) plus the elevate-to-org-admin action.
//
// Routes (all gated on tenant-admin OR platform-admin):
//
//	GET    /api/v1/tenant/users
//	POST   /api/v1/tenant/users/invite
//	POST   /api/v1/tenant/users/{user_id}/disable
//	DELETE /api/v1/tenant/users/{user_id}/disable
//	POST   /api/v1/tenant/users/{user_id}/elevate/{org}
//
// The disable POST/DELETE pair mirrors the existing /pin pattern on
// tag immutability — same code path with a boolean flip, two routes
// so the FE doesn't need to send a body for a destructive action.
//
// Elevate-to-org-admin is the FE's escape hatch: a tenant-admin who
// needs to operate on org content can self-grant the admin role on
// one specific org. The action goes through the existing GrantRole
// RPC; the only thing new here is the calling RBAC posture (tenant-
// admin instead of org-admin). The audit trail captures the
// elevation because every GrantRole call emits rbac.role_granted.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ── Response shapes ───────────────────────────────────────────────────

// TenantUserRoleSummary mirrors auth.v1.RoleSummary one-for-one. The
// FE renders a chip strip from these counts; the proto wrapper is
// flattened so the JSON consumer doesn't see proto-specific shapes.
type TenantUserRoleSummary struct {
	OrgAdminCount  int32 `json:"org_admin_count"`
	OrgWriterCount int32 `json:"org_writer_count"`
	OrgReaderCount int32 `json:"org_reader_count"`
	RepoGrantCount int32 `json:"repo_grant_count"`
	TenantAdmin    bool  `json:"tenant_admin"`
	PlatformAdmin  bool  `json:"platform_admin"`
}

// TenantUserResponse is the JSON wire shape of one row from
// ListTenantUsers. last_login_at is a *time.Time so JSON omits it
// when the user has never logged in (frontends render "Never" in
// that case).
type TenantUserResponse struct {
	UserID      string                `json:"user_id"`
	Username    string                `json:"username"`
	DisplayName string                `json:"display_name"`
	Email       string                `json:"email"`
	Kind        string                `json:"kind"`
	Status      string                `json:"status"`
	LastLoginAt *time.Time            `json:"last_login_at,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
	Roles       TenantUserRoleSummary `json:"roles"`
}

// TenantUsersListResponse wraps the page + cursor + total for the
// top-level GET response.
type TenantUsersListResponse struct {
	Users         []TenantUserResponse `json:"users"`
	NextPageToken string               `json:"next_page_token,omitempty"`
	TotalCount    int32                `json:"total_count"`
}

// InviteUserRequest is the JSON body for POST /tenant/users/invite.
// initial_org_role + initial_org_name are paired — set both or
// neither. expires_in_secs is optional; the BFF clamps it server-side
// before forwarding so the FE doesn't need to know the limit.
type InviteUserRequestBody struct {
	Email          string `json:"email"`
	DisplayName    string `json:"display_name"`
	InitialOrgRole string `json:"initial_org_role,omitempty"`
	InitialOrgName string `json:"initial_org_name,omitempty"`
	ExpiresInSecs  int64  `json:"expires_in_secs,omitempty"`
}

// InviteUserResponseBody surfaces the new user_id + the raw single-use
// invite token in JSON. The token is shown to the operator ONCE in the
// FE invite dialog and never persisted on the client — same discipline
// as the api-key creation flow.
type InviteUserResponseBody struct {
	UserID          string    `json:"user_id"`
	InviteToken     string    `json:"invite_token"`
	InviteExpiresAt time.Time `json:"invite_expires_at"`
}

// SetUserDisabledResponseBody is the small flip-confirmation body.
type SetUserDisabledResponseBody struct {
	Status string `json:"status"` // resulting 'active' | 'disabled'
}

// ── RBAC gate ────────────────────────────────────────────────────────

// isTenantAdminOrPlatformAdmin returns true when the caller holds admin on
// the tenant scope OR is an effective global admin (Phase 5.1 typed primitive
// users.is_global_admin — and in single mode, any tenant admin). Used as the
// gate on every FUT-012 BFF route.
//
// Phase 5.1 tail #2 (2026-06-29): the legacy `(admin, org, "*")` marker check
// was removed when #134 deleted that grant pattern across the codebase. A
// brand-new bootstrap admin holds users.is_global_admin=true with no role
// assignments; without the effectiveGlobalAdmin short-circuit they got 403
// on every tenant-users route. Same shape as the hot-fix applied to
// requireDomainAdmin / requireWebhookAdmin / requireScanPolicyAdmin in #193 —
// see handler.go:requireDomainAdmin for the full rationale.
func (h *Handler) isTenantAdminOrPlatformAdmin(r *http.Request) bool {
	if h.effectiveGlobalAdmin(r) {
		return true
	}
	assignments := h.getUserAssignments(r)
	tenantID := middleware.TenantIDFromContext(r.Context())
	return hasScopedRole(assignments, "tenant", tenantID, "admin")
}

// ── handlers ──────────────────────────────────────────────────────────

func (h *Handler) handleListTenantUsers(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	q := r.URL.Query()
	var pageSize int32
	if s := q.Get("page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "page_size must be 1..200")
			return
		}
		pageSize = int32(n) //nolint:gosec // bounded above
	}
	pageToken := q.Get("page_token")

	resp, err := h.auth.ListTenantUsers(r.Context(), &authv1.ListTenantUsersRequest{
		TenantId:  tenantID,
		PageSize:  pageSize,
		PageToken: pageToken,
	})
	if err != nil {
		slog.Error("ListTenantUsers", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenant users")
		return
	}

	users := make([]TenantUserResponse, 0, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		var lastLogin *time.Time
		if ts := u.GetLastLoginAt(); ts != nil {
			t := ts.AsTime()
			lastLogin = &t
		}
		users = append(users, TenantUserResponse{
			UserID:      u.GetUserId(),
			Username:    u.GetUsername(),
			DisplayName: u.GetDisplayName(),
			Email:       u.GetEmail(),
			Kind:        u.GetKind(),
			Status:      u.GetStatus(),
			LastLoginAt: lastLogin,
			CreatedAt:   u.GetCreatedAt().AsTime(),
			Roles: TenantUserRoleSummary{
				OrgAdminCount:  u.GetRoles().GetOrgAdminCount(),
				OrgWriterCount: u.GetRoles().GetOrgWriterCount(),
				OrgReaderCount: u.GetRoles().GetOrgReaderCount(),
				RepoGrantCount: u.GetRoles().GetRepoGrantCount(),
				TenantAdmin:    u.GetRoles().GetTenantAdmin(),
				PlatformAdmin:  u.GetRoles().GetPlatformAdmin(),
			},
		})
	}
	writeJSON(w, http.StatusOK, TenantUsersListResponse{
		Users:         users,
		NextPageToken: resp.GetNextPageToken(),
		TotalCount:    resp.GetTotalCount(),
	})
}

func (h *Handler) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body InviteUserRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if body.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if (body.InitialOrgRole == "") != (body.InitialOrgName == "") {
		writeError(w, http.StatusBadRequest, "initial_org_role and initial_org_name must be set together")
		return
	}

	resp, err := h.auth.InviteUser(r.Context(), &authv1.InviteUserRequest{
		TenantId:       tenantID,
		Email:          body.Email,
		DisplayName:    body.DisplayName,
		InvitedBy:      callerID,
		InitialOrgRole: body.InitialOrgRole,
		InitialOrgName: body.InitialOrgName,
		ExpiresInSecs:  body.ExpiresInSecs,
	})
	if err != nil {
		mapTenantUserGRPCError(w, "invite user", err)
		return
	}
	out := InviteUserResponseBody{
		UserID:      resp.GetUserId(),
		InviteToken: resp.GetInviteToken(),
	}
	if ts := resp.GetInviteExpiresAt(); ts != nil {
		out.InviteExpiresAt = ts.AsTime()
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleDisableTenantUser(w http.ResponseWriter, r *http.Request) {
	h.setTenantUserDisabled(w, r, true)
}

func (h *Handler) handleEnableTenantUser(w http.ResponseWriter, r *http.Request) {
	h.setTenantUserDisabled(w, r, false)
}

func (h *Handler) setTenantUserDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	userID := r.PathValue("user_id")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	// Self-disable would lock the caller out of the very route they
	// just used to call this; refuse on the BFF instead of leaving it
	// for the next request to discover.
	if disabled && userID == callerID {
		writeError(w, http.StatusBadRequest, "cannot disable yourself")
		return
	}

	resp, err := h.auth.SetUserDisabled(r.Context(), &authv1.SetUserDisabledRequest{
		TenantId:     tenantID,
		UserId:       userID,
		Disabled:     disabled,
		CallerUserId: callerID,
	})
	if err != nil {
		mapTenantUserGRPCError(w, "set user disabled", err)
		return
	}
	writeJSON(w, http.StatusOK, SetUserDisabledResponseBody{Status: resp.GetStatus()})
}

// handleElevateToOrgAdmin issues a (admin, org, <org>) grant for the
// target user. The caller must already be tenant-admin (or platform-
// admin); the target user is typically the caller themselves but the
// route doesn't restrict that — a tenant-admin can grant org-admin to
// any user in their tenant.
//
// Wraps the existing GrantRole RPC. The role_name is fixed to "admin"
// at this surface — the action is specifically "elevate to org-admin",
// so accepting an arbitrary role would muddle the audit story.
func (h *Handler) handleElevateToOrgAdmin(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	userID := r.PathValue("user_id")
	org := r.PathValue("org")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	if _, err := h.auth.GrantRole(r.Context(), &authv1.GrantRoleRequest{
		TenantId:   tenantID,
		UserId:     userID,
		Role:       "admin",
		ScopeType:  "org",
		ScopeValue: org,
		GrantedBy:  callerID,
	}); err != nil {
		slog.Error("GrantRole (tenant-admin elevation)", "err", err, "target_user", userID, "org", org)
		writeError(w, http.StatusInternalServerError, "failed to elevate to org-admin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── error mapping ────────────────────────────────────────────────────

// mapTenantUserGRPCError converts the typed gRPC error codes the Phase A
// handlers return into the right HTTP status. Keeps the FE side clean:
// 400 means input was wrong, 409 means it conflicts with existing
// state, 412 (FailedPrecondition) means the row is in a state this
// action can't operate on (e.g. invited user passed to disable).
func mapTenantUserGRPCError(w http.ResponseWriter, op string, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, st.Message())
			return
		case codes.AlreadyExists:
			writeError(w, http.StatusConflict, st.Message())
			return
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "user not found")
			return
		case codes.FailedPrecondition:
			writeError(w, http.StatusPreconditionFailed, st.Message())
			return
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(w, http.StatusServiceUnavailable, "auth service unavailable")
		return
	}
	slog.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "failed to "+op)
}
