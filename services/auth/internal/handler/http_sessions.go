// Package handler — http_sessions.go: self-service session-list endpoints under
// /api/v1/users/me/sessions (Tier-1 #1 session management). requireAuth-gated
// with a normal access token; a setup token must not manage sessions (same
// boundary the MFA disable handler enforces).
package handler

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// sessionDTO is the wire shape for one session row. Times are rendered as
// RFC-3339 UTC strings; secrets (there are none on a session row) are never
// surfaced. Current flags the caller's own session so the FE can label it and
// disable its "revoke" button.
type sessionDTO struct {
	Sid          string `json:"sid"`
	DeviceLabel  string `json:"device_label"`
	UserAgent    string `json:"user_agent"`
	IP           string `json:"ip"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
	Current      bool   `json:"current"`
}

// listSessions implements GET /api/v1/users/me/sessions — returns the caller's
// live sessions with the current one flagged. Requires a normal access token.
func (h *HTTPHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, tenantID, err := parseUserAndTenant(claims)
	if err != nil {
		// Malformed JWT — treat as internal rather than leaking the raw claim.
		slog.ErrorContext(r.Context(), "sessions list: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	rows, err := h.svc.ListSessions(r.Context(), userID, tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "sessions list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	// Build the wire payload. Pre-size to len(rows) and always emit a non-nil
	// slice so the JSON is `[]` (not `null`) when the user has no sessions.
	out := make([]sessionDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionDTO{
			Sid: s.SID.String(), DeviceLabel: s.DeviceLabel, UserAgent: s.UserAgent, IP: s.IP,
			CreatedAt:    s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			LastActiveAt: s.LastActiveAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			// The caller's own session is the one whose sid matches the JWT's sid claim.
			Current: s.SID.String() == claims.Sid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// revokeSession implements DELETE /api/v1/users/me/sessions/{sid} — revokes one
// of the caller's own sessions. Ownership is enforced in the service/repo layer
// (RevokeOwned filters by user_id), so a sid belonging to another user comes
// back ok=false and surfaces as 404 — the caller cannot even distinguish
// "absent" from "not yours". Requires a normal access token.
func (h *HTTPHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke session: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	// Validate the path parameter as a UUID before touching the service.
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid session id")
		return
	}
	ok, err := h.svc.RevokeSession(r.Context(), userID, sid)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if !ok {
		// Not owned or already absent — 404 so ownership is never leaked.
		writeError(w, http.StatusNotFound, "NOTFOUND", "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeOtherSessions implements POST /api/v1/users/me/sessions/revoke-others —
// revokes every live session for the caller except the current one ("log out
// everywhere else"). Returns the number of sessions revoked. Requires a normal
// access token.
func (h *HTTPHandler) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke other sessions: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	// Keep the caller's current session. A token without a sid claim (e.g. an
	// API-key-dispatched token) parses to uuid.Nil, so nothing is kept and every
	// session is revoked — the correct "revoke all" behaviour for a non-session token.
	current, err := uuid.Parse(claims.Sid)
	if err != nil {
		current = uuid.Nil // non-session token — nothing to keep
	}
	n, err := h.svc.RevokeOtherSessions(r.Context(), userID, current)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke other sessions failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}
