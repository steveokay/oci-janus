// Package handler provides HTTP and gRPC endpoints for the audit service.
// This file implements the gRPC AuditService — specifically GetBuildHistory,
// which translates audit_events rows into BuildRecord proto messages for consumers
// such as registry-management.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// auditRepo is the subset of repository.Repository used by GRPCHandler,
// defined as an interface so unit tests can inject a fake.
type auditRepo interface {
	GetBuildHistory(ctx context.Context, tenantID uuid.UUID, repoID, tag string, limit int) ([]*repository.BuildHistoryRow, error)
	CountPulls(ctx context.Context, tenantID uuid.UUID, since time.Time) (int64, error)
	GetRepoActivity(
		ctx context.Context,
		tenantID uuid.UUID,
		repositoryName string,
		since time.Time,
		cursorTime time.Time,
		cursorID uuid.UUID,
		eventTypes []string,
		limit int,
	) ([]*repository.RepoActivityRow, error)
}

// defaultActivityEventTypes is the operator-facing allowlist applied when the
// caller does not specify event_types. Internal queue/plumbing events
// (webhook.queued, scan.queued, store.queued, gc.*) are deliberately omitted —
// they're noise to a user looking at "what happened in my repo".
//
// Action values here are the strings written by services/audit's
// eventconsumer.mapEvent, not the RabbitMQ routing keys.
var defaultActivityEventTypes = []string{
	"push.image",
	"delete.manifest",
	"delete.tag",
	"scan.completed",
	"scan.policy_blocked",
	"image.signed",
}

// allowedActivityEventTypes is the full set of action values the activity API
// will accept. Used to reject unknown values so we don't pass a caller-supplied
// string through to the `action = ANY($4)` parameter (defence in depth even
// though the value is bound, not interpolated).
var allowedActivityEventTypes = map[string]struct{}{
	"push.image":          {},
	"delete.manifest":     {},
	"delete.tag":          {},
	"scan.completed":      {},
	"scan.policy_blocked": {},
	"image.signed":        {},
}

// maxActivityLookback caps how far back the caller can ask. Audit retention
// is partition-based (default 90 days), so going deeper than that returns no
// rows and just wastes a query plan.
const maxActivityLookback = 90 * 24 * time.Hour

// maxActivityLimit hard-caps the page size so a single API call can't pull
// hundreds of MB of metadata JSON across the wire.
const maxActivityLimit = 200

// defaultActivityLimit is the default page size when the caller leaves limit at zero.
const defaultActivityLimit = 50

// GRPCHandler implements auditv1.AuditServiceServer.
type GRPCHandler struct {
	auditv1.UnimplementedAuditServiceServer
	repo auditRepo
}

// NewGRPC returns a GRPCHandler backed by repo.
func NewGRPC(repo *repository.Repository) *GRPCHandler {
	return &GRPCHandler{repo: repo}
}

// GetBuildHistory returns push/build audit records for a specific repo and tag.
// It queries audit_events filtered by tenant_id, repo_id (from metadata JSON),
// and tag, returning results ordered newest-first.
func (h *GRPCHandler) GetBuildHistory(ctx context.Context, req *auditv1.GetBuildHistoryRequest) (*auditv1.GetBuildHistoryResponse, error) {
	// Validate tenant_id is a valid UUID to prevent SQL injection via parameterised queries.
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}

	rows, err := h.repo.GetBuildHistory(ctx, tenantUUID, req.GetRepoId(), req.GetTag(), int(req.GetLimit()))
	if err != nil {
		slog.ErrorContext(ctx, "GetBuildHistory query failed",
			"tenant_id", req.GetTenantId(),
			"repo_id", req.GetRepoId(),
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to query build history")
	}

	builds := make([]*auditv1.BuildRecord, 0, len(rows))
	for _, row := range rows {
		builds = append(builds, buildRecordFromRow(row))
	}

	return &auditv1.GetBuildHistoryResponse{
		Builds: builds,
		Total:  int32(len(builds)),
	}, nil
}

// GetDailyPullCount returns the count of pull.image events for the tenant in the
// last 24 hours. Non-zero counts surface on the management dashboard stat tile.
func (h *GRPCHandler) GetDailyPullCount(ctx context.Context, req *auditv1.GetDailyPullCountRequest) (*auditv1.GetDailyPullCountResponse, error) {
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	count, err := h.repo.CountPulls(ctx, tenantUUID, time.Now().Add(-24*time.Hour))
	if err != nil {
		slog.ErrorContext(ctx, "GetDailyPullCount query failed",
			"tenant_id", req.GetTenantId(),
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to count pull events")
	}
	return &auditv1.GetDailyPullCountResponse{Count: count}, nil
}

// GetRepoActivity returns operator-facing audit events for a single repository.
// See proto/audit/v1/audit.proto for the wire contract. The handler:
//   - validates the tenant_id / repository_name inputs (parameterised SQL still
//     wins us defence in depth, but rejecting garbage early keeps logs readable);
//   - applies the operator-facing default allowlist when event_types is empty,
//     and rejects unknown action values otherwise;
//   - clamps `since` to a 90-day look-back and the limit to a 200-row max;
//   - decodes the opaque page_token (base64 of "<RFC3339Nano>|<uuid>") into the
//     (occurred_at, event_id) keyset cursor expected by the repository;
//   - converts each row into a slimmed RepoActivityEvent so the raw payload
//     never crosses the gRPC wire.
func (h *GRPCHandler) GetRepoActivity(ctx context.Context, req *auditv1.GetRepoActivityRequest) (*auditv1.GetRepoActivityResponse, error) {
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	repoName := req.GetRepositoryName()
	if repoName == "" {
		return nil, status.Error(codes.InvalidArgument, "repository_name is required")
	}
	// Cheap shape check — the management service has already validated org/repo
	// individually, so this is just a defence-in-depth backstop against direct
	// gRPC callers.
	if len(repoName) > 256 || strings.ContainsAny(repoName, "\x00 \t\r\n") {
		return nil, status.Error(codes.InvalidArgument, "repository_name has invalid characters")
	}

	// Resolve the event type filter. Unknown values cause an InvalidArgument
	// response so a frontend can't smuggle arbitrary action strings.
	eventTypes, err := resolveActivityEventTypes(req.GetEventTypes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// since: default to 7 days ago, clamp to 90 days max look-back.
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
	// A future `since` is harmless (returns no rows) but the partition planner
	// dislikes pathological ranges, so clamp to "now".
	if since.After(now) {
		since = now
	}

	// limit: default 50, hard cap 200.
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

	// Fetch one extra row to determine whether a next page exists without a
	// second query. The extra row is dropped before returning.
	rows, err := h.repo.GetRepoActivity(ctx, tenantUUID, repoName, since, cursorTime, cursorID, eventTypes, limit+1)
	if err != nil {
		slog.ErrorContext(ctx, "GetRepoActivity query failed",
			"tenant_id", req.GetTenantId(),
			"repository_name", repoName,
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to query repo activity")
	}

	var nextToken string
	if len(rows) > limit {
		last := rows[limit-1]
		nextToken = encodeActivityPageToken(last.OccurredAt, last.ID)
		rows = rows[:limit]
	}

	events := make([]*auditv1.RepoActivityEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, repoActivityEventFromRow(row))
	}

	return &auditv1.GetRepoActivityResponse{
		Events:        events,
		NextPageToken: nextToken,
	}, nil
}

// resolveActivityEventTypes substitutes the operator-facing default when the
// caller passes no event_types. Otherwise every supplied value is checked
// against the allowlist and the input is returned verbatim on success.
func resolveActivityEventTypes(in []string) ([]string, error) {
	if len(in) == 0 {
		// Return a defensive copy so callers can't mutate the package-level slice.
		out := make([]string, len(defaultActivityEventTypes))
		copy(out, defaultActivityEventTypes)
		return out, nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, t := range in {
		if _, ok := allowedActivityEventTypes[t]; !ok {
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

// encodeActivityPageToken serialises the keyset cursor for opaque pagination.
// Format: base64URL("<RFC3339Nano>|<event_id UUID>"). RFC3339Nano preserves
// nanosecond resolution so events that share a millisecond still order
// deterministically with the secondary event_id sort.
func encodeActivityPageToken(ts time.Time, id uuid.UUID) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeActivityPageToken parses an opaque cursor produced by encodeActivityPageToken.
// An empty string is valid and means "first page".
func decodeActivityPageToken(tok string) (time.Time, uuid.UUID, error) {
	if tok == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("decode page_token: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("page_token shape")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("page_token timestamp: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("page_token id: %w", err)
	}
	return ts, id, nil
}

// repoActivityEventFromRow projects the audit row's metadata.raw payload into
// a curated set of public fields plus a single human-readable summary. The raw
// payload is intentionally NOT serialised onto the wire — it can hold internal
// fields that are not safe to expose to a dashboard.
func repoActivityEventFromRow(row *repository.RepoActivityRow) *auditv1.RepoActivityEvent {
	// `metadata` is shaped {"event_id": "...", "raw": <payload>} — see
	// services/audit/internal/eventconsumer.mapEvent.
	var meta struct {
		Raw json.RawMessage `json:"raw"`
	}
	_ = json.Unmarshal(row.Metadata, &meta)

	// Pull out only the fields we want on the wire. Unknown fields are dropped.
	var payload struct {
		Tag            string `json:"tag"`
		ManifestDigest string `json:"manifest_digest"`
		PushedBy       string `json:"pushed_by"`
		Username       string `json:"username"`
		RepositoryName string `json:"repository_name"`
		ScannerName    string `json:"scanner_name"`
	}
	if len(meta.Raw) > 0 {
		_ = json.Unmarshal(meta.Raw, &payload)
	}

	// Prefer the explicit username field if upstream supplies one; fall back
	// to PushedBy for push events. We never expose IP or email here even if
	// upstream payloads grow those fields — keep the projection narrow.
	username := payload.Username
	if username == "" {
		username = payload.PushedBy
	}

	return &auditv1.RepoActivityEvent{
		EventId:       row.ID.String(),
		EventType:     row.Action,
		OccurredAt:    timestamppb.New(row.OccurredAt),
		ActorId:       row.ActorID,
		ActorUsername: username,
		Tag:           payload.Tag,
		Digest:        payload.ManifestDigest,
		Outcome:       row.Outcome,
		Summary:       summariseActivity(row.Action, row.Outcome, payload.RepositoryName, payload.Tag, payload.ManifestDigest),
	}
}

// summariseActivity returns a short sentence the UI can render directly so
// the frontend doesn't have to ship a per-event-type i18n table.
func summariseActivity(action, outcome, repoName, tag, digest string) string {
	target := repoName
	if target != "" && tag != "" {
		target = target + ":" + tag
	}
	if target == "" && digest != "" {
		target = digest
	}
	switch action {
	case "push.image":
		if outcome == "failure" {
			return "Push failed for " + target
		}
		return "Pushed " + target
	case "delete.tag":
		return "Deleted tag " + target
	case "delete.manifest":
		if digest != "" {
			return "Deleted manifest " + digest
		}
		return "Deleted manifest"
	case "scan.completed":
		if outcome == "failure" {
			return "Scan flagged policy violation on " + target
		}
		return "Scan completed for " + target
	case "scan.policy_blocked":
		return "Scan blocked " + target
	case "image.signed":
		return "Image signed " + target
	}
	return action
}

// buildRecordFromRow converts a repository.BuildHistoryRow into the proto wire type.
// Optional metadata fields (commit_hash, duration) are extracted from the JSONB
// metadata column if present; missing fields are left as empty strings.
func buildRecordFromRow(row *repository.BuildHistoryRow) *auditv1.BuildRecord {
	// Map audit outcome ("success"/"failure") to the build status vocabulary.
	buildStatus := "success"
	if row.Outcome == "failure" {
		buildStatus = "failed"
	}

	// Extract optional CI metadata stored in the audit event's metadata JSON.
	var meta struct {
		CommitHash string `json:"commit_hash"`
		Duration   string `json:"duration"`
	}
	if len(row.Metadata) > 0 {
		// Best-effort parse — missing keys leave fields as empty strings.
		_ = json.Unmarshal(row.Metadata, &meta)
	}

	return &auditv1.BuildRecord{
		BuildId:     row.ID.String(),
		Status:      buildStatus,
		CommitHash:  meta.CommitHash,
		TriggeredBy: row.ActorID,
		Duration:    meta.Duration,
		OccurredAt:  timestamppb.New(row.OccurredAt),
	}
}
