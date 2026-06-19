// Package handler — repo_activity.go
//
// FE-API-004 — GET /api/v1/repositories/{org}/{repo}/activity
//
// Returns a paginated, repo-scoped slice of the audit log so the dashboard can
// render a "recent activity" feed for an image. Lives in its own file so
// parallel agents touching the main handler.go don't conflict with this route
// during Sprint 6.
//
// Authorization mirrors the existing repo-detail routes (GET tags, GET scan):
// the caller must hold at least the reader role on the repo or its parent org.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ActivityEventResponse is the JSON wire form of one audit event for the
// repo-activity feed. Fields mirror auditv1.RepoActivityEvent but drop the
// proto wrappers so the frontend doesn't need to know the gRPC shape.
//
// Deliberately narrow: no actor IP, no full payload, no internal scan metadata.
// New fields are additive; renaming is a breaking API change.
type ActivityEventResponse struct {
	EventID        string                 `json:"event_id"`
	EventType      string                 `json:"event_type"`
	OccurredAt     time.Time              `json:"occurred_at"`
	ActorID        string                 `json:"actor_id"`
	ActorUsername  string                 `json:"actor_username"`
	Tag            string                 `json:"tag"`
	Digest         string                 `json:"digest"`
	Outcome        string                 `json:"outcome"`
	Summary        string                 `json:"summary"`
	PayloadSummary map[string]interface{} `json:"payload_summary"`
}

// ActivityResponse is the top-level JSON envelope.
type ActivityResponse struct {
	Events        []ActivityEventResponse `json:"events"`
	NextPageToken string                  `json:"next_page_token,omitempty"`
}

// activityMaxLimit is the same hard cap the audit service enforces — kept here
// so we reject obviously-bogus values without a round trip.
const activityMaxLimit = 200

// activityDefaultLimit matches the audit service default; used when the
// caller leaves `limit` empty.
const activityDefaultLimit = 50

// allowedActivityEventTypes mirrors the audit service's allowlist so we can
// reject unknown values at the edge with a clear 400. Keep these two lists in
// sync — audit will also reject anything not on its list.
var allowedActivityEventTypes = map[string]struct{}{
	"push.image":          {},
	"delete.manifest":     {},
	"delete.tag":          {},
	"scan.completed":      {},
	"scan.policy_blocked": {},
	"image.signed":        {},
}

// handleListRepoActivity serves GET /api/v1/repositories/{org}/{repo}/activity.
// The route is mounted from Handler.Register in handler.go.
func (h *Handler) handleListRepoActivity(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// PENTEST-006 parity with /tags and /scan: callers need at least reader on
	// the repo (org-scoped grant counts via the containment rule). Returning
	// 404 — not 403 — so non-members cannot enumerate which repos exist.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Confirm the repo actually exists. Without this we'd happily return an
	// empty activity feed for a typo, which is misleading.
	if _, err := h.findRepo(r, tenantID, org, repoName); err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Parse query params. None are required.
	q := r.URL.Query()

	limit := int32(activityDefaultLimit)
	if s := q.Get("limit"); s != "" {
		n, parseErr := strconv.Atoi(s)
		if parseErr != nil || n <= 0 || n > activityMaxLimit {
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
		// `since` may be in the past; the audit service clamps it to a 90-day
		// look-back. A future `since` is harmless (no rows) but rejecting it
		// catches obviously-broken client clocks early.
		if ts.After(time.Now().Add(5 * time.Minute)) {
			writeError(w, http.StatusBadRequest, "since must not be in the future")
			return
		}
		sincePB = timestamppb.New(ts)
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
			if _, ok := allowedActivityEventTypes[t]; !ok {
				writeError(w, http.StatusBadRequest, "unknown event_type")
				return
			}
			eventTypes = append(eventTypes, t)
		}
	}

	resp, err := h.audit.GetRepoActivity(r.Context(), &auditv1.GetRepoActivityRequest{
		TenantId:       tenantID,
		RepositoryName: org + "/" + repoName,
		Since:          sincePB,
		Limit:          limit,
		PageToken:      pageToken,
		EventTypes:     eventTypes,
	})
	if err != nil {
		slog.Error("GetRepoActivity", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to fetch repo activity")
		return
	}

	out := ActivityResponse{
		Events:        make([]ActivityEventResponse, 0, len(resp.GetEvents())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for _, e := range resp.GetEvents() {
		out.Events = append(out.Events, ActivityEventResponse{
			EventID:        e.GetEventId(),
			EventType:      e.GetEventType(),
			OccurredAt:     e.GetOccurredAt().AsTime(),
			ActorID:        e.GetActorId(),
			ActorUsername:  e.GetActorUsername(),
			Tag:            e.GetTag(),
			Digest:         e.GetDigest(),
			Outcome:        e.GetOutcome(),
			Summary:        e.GetSummary(),
			PayloadSummary: buildPayloadSummary(e),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// buildPayloadSummary collects the curated payload fields into a small map for
// the dashboard. The audit service already strips everything except a
// hand-picked set; we just re-shape it here so consumers don't have to know
// which top-level fields belong to which event_type. Empty strings are dropped
// so the JSON stays compact.
func buildPayloadSummary(e *auditv1.RepoActivityEvent) map[string]interface{} {
	m := map[string]interface{}{}
	if v := e.GetTag(); v != "" {
		m["tag"] = v
	}
	if v := e.GetDigest(); v != "" {
		m["digest"] = v
	}
	if v := e.GetActorUsername(); v != "" {
		m["username"] = v
	}
	if v := e.GetOutcome(); v != "" {
		m["outcome"] = v
	}
	return m
}
