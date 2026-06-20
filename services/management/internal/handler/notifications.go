// Package handler — notifications.go
//
// FE-API-008 — GET /api/v1/notifications
//
// Polled by the topbar bell + notifications drawer to surface recent
// tenant-wide events (push, scan, delete, sign, webhook failures). The
// backend has no per-user read state — clients persist a last_seen_at
// locally and pass it as `since` to compute their own unread count.
// `unread_count` on the response is just len(notifications) for the page.
//
// Authorization: any authenticated tenant user. There is no per-repo RBAC
// check — the response is already tenant-scoped (the gRPC server enforces
// tenant_id) and notifications are operator-facing, not secret. We do not
// surface a notification's full payload over the wire — only the rendered
// title/summary/link plus a small metadata bag.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// NotificationResponse is the JSON wire form of one notification. Fields
// mirror auditv1.NotificationEvent but drop the proto wrappers so the
// frontend doesn't need to know the gRPC shape. Deliberately narrow.
type NotificationResponse struct {
	EventID       string            `json:"event_id"`
	EventType     string            `json:"event_type"`
	OccurredAt    time.Time         `json:"occurred_at"`
	ActorID       string            `json:"actor_id"`
	ActorUsername string            `json:"actor_username"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Link          string            `json:"link"`
	Metadata      map[string]string `json:"metadata"`
}

// NotificationsResponse is the top-level JSON envelope.
type NotificationsResponse struct {
	Notifications []NotificationResponse `json:"notifications"`
	NextPageToken string                 `json:"next_page_token,omitempty"`
	// UnreadCount is the count of notifications in THIS page; the backend
	// does not store per-user read state — see notifications.go header.
	UnreadCount int32 `json:"unread_count"`
}

// notificationsMaxLimit matches the audit service cap so we reject obviously
// bogus values without a round-trip.
const notificationsMaxLimit = 200

// notificationsDefaultLimit matches the audit service default.
const notificationsDefaultLimit = 50

// allowedNotificationEventTypes mirrors the audit service's allowlist so we
// can reject unknown values at the edge with a clear 400. Keep these two
// lists in sync — audit will also reject anything not on its list.
var allowedNotificationEventTypes = map[string]struct{}{
	"push.image":              {},
	"push.failed":             {},
	"delete.manifest":         {},
	"delete.tag":              {},
	"scan.completed":          {},
	"scan.policy_blocked":     {},
	"image.signed":            {},
	"webhook.delivery_failed": {},
}

// handleListNotifications serves GET /api/v1/notifications.
// The route is mounted from Handler.Register in handler.go.
func (h *Handler) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	q := r.URL.Query()

	// limit: default 50, cap 200. Reject out-of-range values with a 400 so a
	// caller passing limit=99999 sees the problem rather than a clamped page.
	limit := int32(notificationsDefaultLimit)
	if s := q.Get("limit"); s != "" {
		n, parseErr := strconv.Atoi(s)
		if parseErr != nil || n <= 0 || n > notificationsMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = int32(n)
	}

	pageToken := q.Get("page_token")
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	var sincePB *timestamppb.Timestamp
	if s := q.Get("since"); s != "" {
		ts, parseErr := time.Parse(time.RFC3339, s)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		// A future `since` would silently return zero notifications, which
		// looks like "you're up to date" when in fact the client clock is
		// broken — reject it so the bug surfaces.
		if ts.After(time.Now().Add(5 * time.Minute)) {
			writeError(w, http.StatusBadRequest, "since must not be in the future")
			return
		}
		sincePB = timestamppb.New(ts)
	}

	// unread_only: when true the handler treats the supplied `since` as a
	// last_seen_at cursor (the frontend's own read pointer). The backend
	// stores no per-user read state; the boolean simply means "I gave you a
	// since and I really want only newer rows". If the caller forgot to
	// pass `since`, fall back to the audit service default (7 days).
	// Documenting this here so the frontend doesn't need to guess.
	if v := q.Get("unread_only"); v != "" {
		if v != "true" && v != "false" && v != "1" && v != "0" {
			writeError(w, http.StatusBadRequest, "unread_only must be a boolean")
			return
		}
		// The flag is informational at the BFF — the audit-side `since`
		// already drives the row filter. Kept here so a future iteration
		// can add server-stored read state without changing the wire shape.
	}

	var eventTypes []string
	if s := q.Get("event_types"); s != "" {
		// CSV split. Reject any unknown value here so we never proxy a
		// caller-supplied action string to the audit service unchecked.
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if _, ok := allowedNotificationEventTypes[t]; !ok {
				writeError(w, http.StatusBadRequest, "unknown event_type")
				return
			}
			eventTypes = append(eventTypes, t)
		}
	}

	resp, err := h.audit.GetNotifications(r.Context(), &auditv1.GetNotificationsRequest{
		TenantId:   tenantID,
		Since:      sincePB,
		Limit:      limit,
		PageToken:  pageToken,
		EventTypes: eventTypes,
	})
	if err != nil {
		slog.Error("GetNotifications", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to fetch notifications")
		return
	}

	out := NotificationsResponse{
		Notifications: make([]NotificationResponse, 0, len(resp.GetNotifications())),
		NextPageToken: resp.GetNextPageToken(),
		UnreadCount:   resp.GetUnreadCount(),
	}
	for _, n := range resp.GetNotifications() {
		out.Notifications = append(out.Notifications, NotificationResponse{
			EventID:       n.GetEventId(),
			EventType:     n.GetEventType(),
			OccurredAt:    n.GetOccurredAt().AsTime(),
			ActorID:       n.GetActorId(),
			ActorUsername: n.GetActorUsername(),
			Title:         n.GetTitle(),
			Summary:       n.GetSummary(),
			Link:          n.GetLink(),
			Metadata:      n.GetMetadata(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
