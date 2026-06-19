// Package handler — this file holds the /api/v1/users/me HTTP endpoints
// (FE-API-011, FE-API-012, FE-API-013). They live in the auth service because
// the same JWT middleware and Service value already used by /login and
// /apikeys give us the cheapest, mTLS-free path to the user record.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// currentUserResponse is the shape returned by GET /api/v1/users/me and
// PATCH /api/v1/users/me. Nullable fields use *string / *time.Time so that
// the JSON encodes as null rather than the zero value, which lets the dashboard
// distinguish "not set" from "set to empty string".
type currentUserResponse struct {
	UserID      string       `json:"user_id"`
	Username    string       `json:"username"`
	Email       *string      `json:"email"`
	DisplayName *string      `json:"display_name"`
	CreatedAt   time.Time    `json:"created_at"`
	LastLoginAt *time.Time   `json:"last_login_at"`
	TenantID    string       `json:"tenant_id"`
	Roles       []string     `json:"roles"`
	Memberships []membership `json:"memberships"`
}

// membership describes one row from role_assignments as the dashboard needs it.
type membership struct {
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
	Role       string `json:"role"`
}

// getCurrentUser implements FE-API-011 — returns the authenticated user's
// profile, roles, and memberships in a single response. Reads `sub` from the
// validated JWT; never trusts a tenant/user ID from the request body.
func (h *HTTPHandler) getCurrentUser(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, tenantID, err := parseUserAndTenant(claims)
	if err != nil {
		// Malformed JWT — should not happen with our own signer but treat as
		// an internal error rather than letting the user see the raw claim.
		slog.ErrorContext(r.Context(), "users/me: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	user, err := h.svc.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// JWT subject points to a user that no longer exists. We respond
			// with 401 (rather than 404) so the frontend's interceptor logs
			// the user out instead of treating it as a recoverable error.
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		slog.ErrorContext(r.Context(), "users/me: GetUserByID failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	resp, err := h.buildCurrentUserResponse(r, user, tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users/me: build response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// updateCurrentUser implements FE-API-012 — PATCH /api/v1/users/me.
// Accepts {display_name?, email?}; at least one field is required. Returns
// the refreshed profile (same shape as GET) on success.
func (h *HTTPHandler) updateCurrentUser(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, tenantID, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "users/me PATCH: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Use pointers in the decode struct so we can distinguish "field absent"
	// (nil) from "explicitly cleared" (non-nil pointer to ""). The JSON
	// decoder leaves missing fields as nil.
	var req struct {
		DisplayName *string `json:"display_name,omitempty"`
		Email       *string `json:"email,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	user, err := h.svc.UpdateUserProfile(r.Context(), userID, req.DisplayName, req.Email)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNoFieldsToUpdate):
			writeError(w, http.StatusBadRequest, "BADREQUEST", "at least one field (display_name, email) is required")
		case errors.Is(err, service.ErrInvalidDisplayName):
			writeError(w, http.StatusBadRequest, "BADREQUEST", "display_name must be 1..128 characters and contain no control characters")
		case errors.Is(err, service.ErrInvalidEmail):
			writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid email address")
		case errors.Is(err, repository.ErrAlreadyExists):
			// Email collision with another user in the same tenant.
			writeError(w, http.StatusConflict, "CONFLICT", "email already in use")
		case errors.Is(err, repository.ErrNotFound):
			// JWT subject vanished — same handling as GET.
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		default:
			slog.ErrorContext(r.Context(), "users/me PATCH: update failed", "error", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		}
		return
	}

	resp, err := h.buildCurrentUserResponse(r, user, tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "users/me PATCH: build response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// changeCurrentUserPassword implements FE-API-013 — POST /api/v1/users/me/password.
// Verifies current password, enforces the policy on the new one, persists, and
// revokes every other session for this user. On success returns 204 No Content.
//
// On invalid current_password we MUST return 401 with the same generic body as
// other auth failures — exposing "wrong password" vs "user not found" via the
// status code would let an attacker enumerate accounts even though the JWT
// already proved authentication. PENTEST-005-style guidance applies.
func (h *HTTPHandler) changeCurrentUserPassword(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "users/me/password: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "current_password and new_password are required")
		return
	}

	if err := h.svc.ChangePassword(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, service.ErrPasswordRateLimited):
			writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "too many password change attempts; try again later")
		case errors.Is(err, service.ErrInvalidCredentials):
			// PENTEST-005-style: identical to the /login wrong-password
			// response so an attacker with a stolen JWT cannot cheaply
			// brute-force the password under the same identity.
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		case service.IsPasswordPolicyError(err):
			// Safe to forward verbatim — these are validation messages, not
			// internal errors. See service.IsPasswordPolicyError docstring.
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
		default:
			slog.ErrorContext(r.Context(), "users/me/password: change failed", "error", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parseUserAndTenant pulls the user UUID (from `sub`) and tenant UUID (from
// the custom claim) out of a Claims value, validating both as parseable UUIDs.
// Returns an error rather than panicking so callers can downgrade to 500
// without leaking the malformed value to the client.
func parseUserAndTenant(c *service.Claims) (uuid.UUID, uuid.UUID, error) {
	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	tenantID, err := uuid.Parse(c.TenantID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return userID, tenantID, nil
}

// buildCurrentUserResponse assembles the full /users/me payload by combining
// the user row with the role assignments fetched via the service layer. The
// roles list is the deduplicated set of role names; memberships is the raw
// scope+role list so the dashboard can show "admin of org acme" rather than
// just "admin".
func (h *HTTPHandler) buildCurrentUserResponse(r *http.Request, user *repository.User, tenantID uuid.UUID) (currentUserResponse, error) {
	assignments, err := h.svc.GetUserRoles(r.Context(), user.ID, tenantID)
	if err != nil {
		return currentUserResponse{}, err
	}

	seen := make(map[string]struct{}, len(assignments))
	roles := make([]string, 0, len(assignments))
	memberships := make([]membership, 0, len(assignments))
	for _, a := range assignments {
		if _, ok := seen[a.RoleName]; !ok {
			seen[a.RoleName] = struct{}{}
			roles = append(roles, a.RoleName)
		}
		memberships = append(memberships, membership{
			ScopeType:  a.ScopeType,
			ScopeValue: a.ScopeValue,
			Role:       a.RoleName,
		})
	}
	// Stable sort so the dashboard sees deterministic output regardless of
	// the database's row ordering (important for snapshot-style frontend tests).
	sort.Strings(roles)
	sort.SliceStable(memberships, func(i, j int) bool {
		if memberships[i].ScopeType != memberships[j].ScopeType {
			return memberships[i].ScopeType < memberships[j].ScopeType
		}
		if memberships[i].ScopeValue != memberships[j].ScopeValue {
			return memberships[i].ScopeValue < memberships[j].ScopeValue
		}
		return memberships[i].Role < memberships[j].Role
	})

	// Map empty-string email to nil so the JSON encodes as null — the
	// frontend treats "" and null differently (one is a value, the other is
	// "field not set"). DB stores email as NULL when unset (migration
	// 20260619000001) and the repository COALESCEs to "" for backwards-compat.
	var emailPtr *string
	if user.Email != "" {
		v := user.Email
		emailPtr = &v
	}

	return currentUserResponse{
		UserID:      user.ID.String(),
		Username:    user.Username,
		Email:       emailPtr,
		DisplayName: user.DisplayName,
		CreatedAt:   user.CreatedAt,
		LastLoginAt: user.LastLoginAt,
		TenantID:    user.TenantID.String(),
		Roles:       roles,
		Memberships: memberships,
	}, nil
}
