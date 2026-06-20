// Package handler — notifications.go
//
// FE-API-008 — gRPC GetNotifications RPC.
//
// Returns recent tenant-wide audit events for the management dashboard's
// topbar notification bell. Mechanically very similar to GetRepoActivity in
// grpc.go, but filters by tenant only (no repository_name predicate) and
// projects each row through notificationRenderer to produce a title +
// summary + link suitable for direct UI render — so the frontend doesn't
// need a per-event-type i18n table.
//
// Allowlist + page_token format are shared with GetRepoActivity so the
// frontend can reuse the same cursor encoding across both feeds.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// defaultNotificationEventTypes is the operator-facing allowlist applied when
// the caller does not specify event_types. Mirrors the FE-API-004 default set
// plus webhook delivery failures + push failures so a tenant admin sees
// everything they'd want surfaced in a bell.
//
// Action values here are the strings written by services/audit's
// eventconsumer.mapEvent, NOT the RabbitMQ routing keys. Notably:
//   - push.failed         (added by FE-API-008 in mapEvent)
//   - webhook.delivery_failed (added by FE-API-008 in mapEvent — the
//     dashboard's notification vocabulary, not the routing key
//     "webhook.failed")
var defaultNotificationEventTypes = []string{
	"push.image",
	"push.failed",
	"delete.manifest",
	"delete.tag",
	"scan.completed",
	"scan.policy_blocked",
	"image.signed",
	"webhook.delivery_failed",
}

// allowedNotificationEventTypes is the full set of action values the
// notifications API accepts. Used to reject unknown values so a caller can't
// smuggle an arbitrary string into the parameterised `action = ANY($N)` clause
// — defence in depth even though the value is bound, not interpolated.
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

// GetNotifications returns operator-facing audit events for the calling
// tenant, ordered newest-first, suitable for the management dashboard's
// topbar bell. See proto/audit/v1/audit.proto for the wire contract.
//
// "unread" semantics: the backend does NOT store per-user read state. The
// returned `unread_count` is simply the number of rows returned by this
// call. The frontend persists a `last_seen_at` cursor locally and may pass
// it back as the `since` parameter to compute its own unread count — at
// which point unread_count equals the size of the returned page.
func (h *GRPCHandler) GetNotifications(ctx context.Context, req *auditv1.GetNotificationsRequest) (*auditv1.GetNotificationsResponse, error) {
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	eventTypes, err := resolveNotificationEventTypes(req.GetEventTypes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// since: default to 7 days ago, clamp to 90 days max look-back (matches
	// partition retention so a long look-back never returns useful data).
	now := time.Now()
	since := time.Time{}
	if ts := req.GetSince(); ts != nil {
		since = ts.AsTime()
	}
	if since.IsZero() {
		since = now.Add(-7 * 24 * time.Hour)
	}
	minSince := now.Add(-maxActivityLookback)
	if since.Before(minSince) {
		since = minSince
	}
	if since.After(now) {
		since = now
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}

	cursorTime, cursorID, err := decodeActivityPageToken(req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}

	// Reuse the same fetch-one-extra trick as GetRepoActivity to detect a
	// next page without a follow-up query.
	rows, err := h.repo.GetNotifications(ctx, tenantUUID, since, cursorTime, cursorID, eventTypes, limit+1)
	if err != nil {
		slog.ErrorContext(ctx, "GetNotifications query failed",
			"tenant_id", req.GetTenantId(),
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to query notifications")
	}

	var nextToken string
	if len(rows) > limit {
		last := rows[limit-1]
		nextToken = encodeActivityPageToken(last.OccurredAt, last.ID)
		rows = rows[:limit]
	}

	notifications := make([]*auditv1.NotificationEvent, 0, len(rows))
	for _, row := range rows {
		notifications = append(notifications, notificationFromRow(row))
	}

	return &auditv1.GetNotificationsResponse{
		Notifications: notifications,
		NextPageToken: nextToken,
		// See the method comment — unread_count is the size of this page.
		// The frontend computes its own true-unread count by paging from
		// `since = last_seen_at`.
		UnreadCount: int32(len(notifications)),
	}, nil
}

// resolveNotificationEventTypes substitutes the operator-facing default when
// the caller passes no event_types. Otherwise every supplied value is checked
// against the allowlist; unknown values cause an InvalidArgument error at the
// caller so the SQL layer never sees an unchecked string.
func resolveNotificationEventTypes(in []string) ([]string, error) {
	if len(in) == 0 {
		// Defensive copy so callers can't mutate the package-level slice.
		out := make([]string, len(defaultNotificationEventTypes))
		copy(out, defaultNotificationEventTypes)
		return out, nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, t := range in {
		if _, ok := allowedNotificationEventTypes[t]; !ok {
			return nil, fmt.Errorf("event_type %q is not allowed", t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// rawNotificationPayload is the union of every payload field the renderer
// reads. Unknown fields are dropped during Unmarshal so we can't accidentally
// expose new fields upstream services add later. Keep this list narrow.
type rawNotificationPayload struct {
	RepositoryName string `json:"repository_name"`
	Tag            string `json:"tag"`
	ManifestDigest string `json:"manifest_digest"`
	PushedBy       string `json:"pushed_by"`
	Username       string `json:"username"`
	ScannerName    string `json:"scanner_name"`
	Signer         string `json:"signer"`
	Reason         string `json:"reason"`
	// Webhook-failure fields. Field names mirror the publisher payload
	// (libs/rabbitmq/events) so they line up without translation.
	WebhookID  string `json:"webhook_id"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	Attempts   int    `json:"attempts"`
	// Scan finding counts — used to enrich scan.completed summaries.
	FindingsTotal    int `json:"findings_total"`
	FindingsCritical int `json:"findings_critical"`
}

// notificationFromRow converts an audit row into a wire-shaped notification
// event. The hand-crafted title/summary/link mapping lives here so a single
// switch keeps the renderer easy to extend when new event_types land.
func notificationFromRow(row *repository.NotificationRow) *auditv1.NotificationEvent {
	// metadata is shaped {"event_id": "...", "raw": <payload>} — same wrapper
	// the eventconsumer writes. Errors are intentionally ignored: missing
	// or malformed fields just render as the bare title.
	var meta struct {
		Raw json.RawMessage `json:"raw"`
	}
	_ = json.Unmarshal(row.Metadata, &meta)

	var p rawNotificationPayload
	if len(meta.Raw) > 0 {
		_ = json.Unmarshal(meta.Raw, &p)
	}

	// Prefer an explicit username field when upstream supplies one; fall
	// back to pushed_by for push events. Never expose actor_ip or email
	// from the raw payload even if upstream services grow them.
	username := p.Username
	if username == "" {
		username = p.PushedBy
	}

	org, repo := splitOrgRepo(p.RepositoryName)

	title, summary, link := renderNotification(row.Action, row.Outcome, &p, org, repo, username)

	return &auditv1.NotificationEvent{
		EventId:       row.ID.String(),
		EventType:     row.Action,
		OccurredAt:    timestamppb.New(row.OccurredAt),
		ActorId:       row.ActorID,
		ActorUsername: username,
		Title:         title,
		Summary:       summary,
		Link:          link,
		Metadata:      notificationMetadataMap(&p),
	}
}

// renderNotification is the hand-crafted (event_type → title, summary, link)
// table. Missing payload fields render as empty in the summary; the UI
// falls back to the title alone.
//
// Templates here match the table in the FE-API-008 spec. Keep this in sync
// with the spec when adding new event types.
func renderNotification(action, outcome string, p *rawNotificationPayload, org, repo, username string) (title, summary, link string) {
	repoPath := org + "/" + repo
	if org == "" || repo == "" {
		repoPath = "" // avoid emitting "/" for tenant-wide events with no repo context
	}
	tagPath := ""
	if repoPath != "" && p.Tag != "" {
		tagPath = repoPath + ":" + p.Tag
	}

	switch action {
	case "push.image":
		title = "Push completed"
		if tagPath != "" {
			summary = tagPath + " pushed"
		}
		if repoPath != "" && p.Tag != "" {
			link = "/repositories/" + repoPath + "/tags/" + p.Tag
		} else if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		return

	case "push.failed":
		title = "Push failed"
		if p.Reason != "" {
			summary = p.Reason
		} else if tagPath != "" {
			summary = tagPath
		}
		if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		return

	case "scan.completed":
		title = "Scan completed"
		if tagPath != "" {
			summary = fmt.Sprintf("%s — %d findings", tagPath, p.FindingsTotal)
		} else if p.ManifestDigest != "" {
			summary = fmt.Sprintf("%s — %d findings", p.ManifestDigest, p.FindingsTotal)
		}
		if repoPath != "" && p.Tag != "" {
			link = "/repositories/" + repoPath + "/tags/" + p.Tag
		} else if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		// Scans that flag policy violations come through with outcome=failure.
		// Override the title so the bell card matches the verdict.
		if outcome == "failure" {
			title = "Scan flagged policy violation"
		}
		return

	case "scan.policy_blocked":
		title = "Push blocked by policy"
		if tagPath != "" {
			summary = fmt.Sprintf("%s — %d critical findings blocked", tagPath, p.FindingsCritical)
		}
		if repoPath != "" && p.Tag != "" {
			link = "/repositories/" + repoPath + "/tags/" + p.Tag
		} else if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		return

	case "delete.manifest":
		title = "Manifest deleted"
		if repoPath != "" && p.ManifestDigest != "" {
			summary = repoPath + "@" + p.ManifestDigest
		} else if p.ManifestDigest != "" {
			summary = p.ManifestDigest
		}
		if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		return

	case "delete.tag":
		title = "Tag deleted"
		if tagPath != "" {
			summary = tagPath
		}
		if repoPath != "" {
			link = "/repositories/" + repoPath
		}
		return

	case "image.signed":
		title = "Image signed"
		signer := p.Signer
		if signer == "" {
			signer = username
		}
		if tagPath != "" && signer != "" {
			summary = tagPath + " signed by " + signer
		} else if tagPath != "" {
			summary = tagPath
		}
		if repoPath != "" && p.Tag != "" {
			link = "/repositories/" + repoPath + "/tags/" + p.Tag
		}
		return

	case "webhook.delivery_failed":
		title = "Webhook failed"
		switch {
		case p.URL != "" && p.StatusCode != 0:
			summary = fmt.Sprintf("%s — %d", p.URL, p.StatusCode)
		case p.URL != "":
			summary = p.URL
		}
		if p.WebhookID != "" {
			link = "/webhooks/" + p.WebhookID
		}
		return

	case "webhook.delivery_dead":
		title = "Webhook dead-lettered"
		if p.URL != "" && p.Attempts > 0 {
			summary = fmt.Sprintf("%s after %d attempts", p.URL, p.Attempts)
		} else if p.URL != "" {
			summary = p.URL
		}
		if p.WebhookID != "" {
			link = "/webhooks/" + p.WebhookID
		}
		return
	}

	// Unknown action falls through. We still return a usable title so the UI
	// renders something rather than a blank row.
	title = action
	return
}

// notificationMetadataMap returns the small key/value bag carried on the wire
// for the dashboard to render extra context. Empty values are dropped to keep
// the JSON tight; numeric fields are stringified so the proto type stays a
// simple string→string map.
func notificationMetadataMap(p *rawNotificationPayload) map[string]string {
	m := map[string]string{}
	if p.RepositoryName != "" {
		m["repo"] = p.RepositoryName
	}
	if p.Tag != "" {
		m["tag"] = p.Tag
	}
	if p.ManifestDigest != "" {
		m["digest"] = p.ManifestDigest
	}
	if p.WebhookID != "" {
		m["webhook_id"] = p.WebhookID
	}
	if p.URL != "" {
		m["url"] = p.URL
	}
	if p.StatusCode != 0 {
		m["status_code"] = fmt.Sprintf("%d", p.StatusCode)
	}
	if p.Attempts != 0 {
		m["attempts"] = fmt.Sprintf("%d", p.Attempts)
	}
	if p.FindingsTotal != 0 {
		m["findings_total"] = fmt.Sprintf("%d", p.FindingsTotal)
	}
	if p.FindingsCritical != 0 {
		m["findings_critical"] = fmt.Sprintf("%d", p.FindingsCritical)
	}
	return m
}

// splitOrgRepo splits a canonical "org/repo" string. Returns empty strings
// when the payload doesn't carry a repository_name (tenant-wide events such
// as webhook failures). The renderer guards on the empty case so an empty
// payload never produces a malformed "/" link.
func splitOrgRepo(name string) (org, repo string) {
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			return name[:i], name[i+1:]
		}
	}
	return "", ""
}
