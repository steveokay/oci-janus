// http_access_activity.go — HTTP handler for GET /api/v1/access/activity (FE-API-048, Task 15).
//
// Thin facade over service.ActivityService.List. Authorization rules (spec §5.3):
//   - Caller must be authenticated (JWT or API key).
//   - Workspace-admin (admin or owner role in the caller's tenant) may query
//     any principal_user_id that belongs to the same tenant.
//   - Non-admin callers may only query their own user ID.
//   - All negative paths (cross-tenant, non-admin querying other, not found)
//     return 404 with body {"error":"NOT_FOUND"} — never 403.
package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// RegisterAccessActivity mounts the activity feed route onto mux.
// Always registered; returns 501 when activityService is nil (not wired).
func (h *HTTPHandler) RegisterAccessActivity(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/access/activity", h.getAccessActivity)
}

// getAccessActivity handles GET /api/v1/access/activity.
//
// Query parameters:
//   - principal_user_id — UUID of the user whose activity to fetch. Defaults to
//     the caller's own user ID when absent.
//   - limit — maximum events to return per page (default 50, max 200).
//   - page_token — opaque cursor for the next page (pass the value from the
//     previous response's next_page_token field).
//   - since — RFC3339 lower bound on event time. When present, the feed is
//     time-bounded server-side (the FE date-range chips use this). Absent ⇒
//     the audit service's default window (7 days). Malformed ⇒ 400.
//
// Response envelope:
//
//	{"activity": [...], "next_page_token": "<string or empty>"}
func (h *HTTPHandler) getAccessActivity(w http.ResponseWriter, r *http.Request) {
	// Gate: activity service must be wired via WithActivityService.
	if h.activityService == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "activity not configured")
		return
	}

	// Gate: caller must be authenticated.
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	// Parse caller identity from JWT claims.
	callerID, err := uuid.Parse(claims.Subject)
	if err != nil {
		slog.ErrorContext(r.Context(), "activity: caller token has invalid sub", "value", claims.Subject)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	callerTenant, err := uuid.Parse(claims.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "activity: caller token has invalid tenant_id", "value", claims.TenantID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Resolve target principal. Default to the caller's own ID when omitted.
	principalParam := r.URL.Query().Get("principal_user_id")
	if principalParam == "" {
		principalParam = callerID.String()
	}
	targetID, err := uuid.Parse(principalParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid principal_user_id")
		return
	}

	// Parse limit (default 50, cap at 200). Use the same digit-by-digit
	// validation style as listServiceAccounts to avoid strconv dependency.
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n := 0
		valid := true
		for _, ch := range raw {
			if ch < '0' || ch > '9' {
				valid = false
				break
			}
			n = n*10 + int(ch-'0')
		}
		if !valid || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "limit must be between 1 and 200")
			return
		}
		limit = n
	}

	pageToken := r.URL.Query().Get("page_token")

	// Parse the optional `since` lower bound (RFC3339). When present it replaces
	// the old limit-as-time-proxy approximation with a real server-side time
	// filter (FUT-088 #1). A malformed value is a client error — reject it
	// rather than silently falling back to the default window, which would make
	// a typo'd timestamp look like it "worked" but return the wrong range.
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "since must be an RFC3339 timestamp")
			return
		}
		since = parsed
	}

	// Determine admin status. callerIsTenantAdmin is fail-closed (returns false
	// on lookup error) which is the correct security posture here. SA bearers
	// (claims.PrincipalKind == "service_account") are also denied admin
	// authority — only attestable human identities clear the gate.
	isAdmin := callerIsTenantAdmin(r.Context(), h.svc, callerID, callerTenant, claims.PrincipalKind)

	// Delegate to the service layer. The service enforces the ordering-of-checks
	// from spec §5.3: (1) resolve target, (2) tenant check, (3) non-admin check.
	// All negative paths from the service return codes.NotFound so we map them to
	// HTTP 404 {"error":"NOT_FOUND"} regardless of the underlying reason — never 403.
	activities, nextToken, err := h.activityService.List(r.Context(), service.ListActivityOpts{
		CallerUserID:   callerID,
		CallerTenantID: callerTenant,
		CallerIsAdmin:  isAdmin,
		TargetUserID:   targetID,
		PageSize:       int32(limit),
		PageToken:      pageToken,
		Since:          since,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Return 404 with a flat {"error":"NOT_FOUND"} body per spec §5.3.
			// Using writeJSON directly (not writeError) so the body shape is exactly
			// {"error":"NOT_FOUND"} — the spec requires this specific flat shape to
			// make all negative paths look identical at the response level.
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "NOT_FOUND"})
			return
		}
		slog.ErrorContext(r.Context(), "activity: List failed", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Ensure the activity slice is never serialised as JSON null so callers
	// always receive an array (possibly empty) rather than a null field.
	if activities == nil {
		activities = []service.PrincipalActivity{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"activity":        activities,
		"next_page_token": nextToken,
	})
}
