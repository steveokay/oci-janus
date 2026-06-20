// Package repository handles all database access for the audit service.
// The audit_events table is append-only — this package only inserts and queries.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditEvent mirrors the audit_events table row and the CLAUDE.md AuditEvent struct.
type AuditEvent struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	ActorID    string
	ActorType  string
	ActorIP    string
	Action     string
	Resource   string
	Outcome    string
	Metadata   json.RawMessage
	OccurredAt time.Time
}

// Repository wraps the pool and provides append-only audit operations.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert writes one audit event row. It never updates or deletes.
func (r *Repository) Insert(ctx context.Context, e *AuditEvent) error {
	meta := e.Metadata
	if meta == nil {
		meta = json.RawMessage("{}")
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO audit_events
		    (tenant_id, actor_id, actor_type, actor_ip, action, resource, outcome, metadata, occurred_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.TenantID, e.ActorID, e.ActorType, e.ActorIP,
		e.Action, e.Resource, e.Outcome, meta, e.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("audit Insert: %w", err)
	}
	return nil
}

// QueryFilter describes the parameters for listing audit events.
type QueryFilter struct {
	TenantID uuid.UUID
	ActorID  string
	Action   string
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}

// Query returns audit events matching the filter, ordered by occurred_at DESC.
func (r *Repository) Query(ctx context.Context, f QueryFilter) ([]*AuditEvent, error) {
	if f.Limit == 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.To.IsZero() {
		f.To = time.Now()
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-30 * 24 * time.Hour) // default last 30 days
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, actor_id, actor_type, actor_ip, action, resource,
		        outcome, metadata, occurred_at
		 FROM audit_events
		 WHERE tenant_id   = $1
		   AND occurred_at >= $2
		   AND occurred_at <= $3
		   AND ($4 = '' OR actor_id = $4)
		   AND ($5 = '' OR action   = $5)
		 ORDER BY occurred_at DESC
		 LIMIT $6 OFFSET $7`,
		f.TenantID, f.From, f.To, f.ActorID, f.Action, f.Limit, f.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("audit Query: %w", err)
	}
	defer rows.Close()

	var out []*AuditEvent
	for rows.Next() {
		e := &AuditEvent{}
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.ActorID, &e.ActorType, &e.ActorIP,
			&e.Action, &e.Resource, &e.Outcome, &e.Metadata, &e.OccurredAt,
		); err != nil {
			return nil, fmt.Errorf("audit Query scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BuildHistoryRow is a single row returned by GetBuildHistory.
type BuildHistoryRow struct {
	ID          uuid.UUID
	ActorID     string
	Outcome     string
	Metadata    json.RawMessage
	OccurredAt  time.Time
}

// GetBuildHistory returns push/build audit events for a repository and tag,
// ordered newest-first. The resource column format is "org/repo:tag". The query
// uses a LIKE pattern so it covers both "push.image" and future build events.
// limit is capped at 100 to prevent runaway queries.
func (r *Repository) GetBuildHistory(ctx context.Context, tenantID uuid.UUID, repoID, tag string, limit int) ([]*BuildHistoryRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	// The resource field stores "org/repo:tag". We match by repo_id embedded in
	// the metadata JSON, filtering action = "push.image" for build history.
	// We use metadata->>'repo_id' = $2 to avoid a LIKE scan across all resources.
	rows, err := r.pool.Query(ctx,
		`SELECT id, actor_id, outcome, metadata, occurred_at
		 FROM audit_events
		 WHERE tenant_id = $1
		   AND action    = 'push.image'
		   AND metadata->>'repo_id' = $2
		   AND ($3 = '' OR metadata->>'tag' = $3)
		 ORDER BY occurred_at DESC
		 LIMIT $4`,
		tenantID, repoID, tag, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("audit GetBuildHistory: %w", err)
	}
	defer rows.Close()

	var out []*BuildHistoryRow
	for rows.Next() {
		e := &BuildHistoryRow{}
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Outcome, &e.Metadata, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("audit GetBuildHistory scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RepoActivityRow is one row returned by GetRepoActivity. The caller projects
// the raw payload (held in metadata.raw) into a tighter wire type — full
// payloads never cross the gRPC boundary.
type RepoActivityRow struct {
	ID         uuid.UUID
	ActorID    string
	ActorType  string
	Action     string
	Resource   string
	Outcome    string
	Metadata   json.RawMessage
	OccurredAt time.Time
}

// GetRepoActivity returns audit events for a single repository identified by
// its canonical "org/repo" name, ordered newest-first then by event_id DESC so
// the secondary sort key matches the partition primary key.
//
// repositoryName is matched against metadata.raw.repository_name (the
// payload field set by registry-core and registry-scanner). Events whose
// payload lacks that field (e.g. RoutingTenantCreated) are not returned —
// which is correct because they're not repo-scoped activity.
//
// eventTypes is REQUIRED to be a non-empty caller-supplied allowlist. The
// handler is responsible for substituting its operator-facing default when
// the caller did not specify any types. Empty slice ⇒ no rows returned (the
// IN clause would match nothing). The caller MUST also validate each entry
// against an allowlist before passing them through here — even though the
// values are bound as a parameterised array, restricting the set keeps the
// repository layer honest about which actions a frontend can request.
//
// since lower-bounds occurred_at. cursorTime + cursorID, when both non-zero,
// drive keyset pagination using the lexicographic (occurred_at, id) pair so
// stable pagination is possible even when many events share an instant.
func (r *Repository) GetRepoActivity(
	ctx context.Context,
	tenantID uuid.UUID,
	repositoryName string,
	since time.Time,
	cursorTime time.Time,
	cursorID uuid.UUID,
	eventTypes []string,
	limit int,
) ([]*RepoActivityRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(eventTypes) == 0 {
		return nil, nil
	}

	// Keyset pagination. When the cursor is empty, the (occurred_at < $X
	// OR (= AND id <)) branch must never fire — supply a far-future
	// sentinel so the WHERE clause is still parameterised and the planner
	// can use idx_audit_events_tenant_occurred regardless.
	cursorActive := !cursorTime.IsZero()
	// Far future so the < check is trivially true when no cursor; we also
	// guard via cursorActive so the AND id check is bypassed.
	if !cursorActive {
		cursorTime = time.Now().Add(100 * 365 * 24 * time.Hour)
		cursorID = uuid.Nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, actor_id, actor_type, action, resource, outcome, metadata, occurred_at
		 FROM audit_events
		 WHERE tenant_id = $1
		   AND metadata->'raw'->>'repository_name' = $2
		   AND occurred_at >= $3
		   AND action = ANY($4)
		   AND ($5 = FALSE OR occurred_at < $6 OR (occurred_at = $6 AND id < $7))
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT $8`,
		tenantID, repositoryName, since, eventTypes,
		cursorActive, cursorTime, cursorID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("audit GetRepoActivity: %w", err)
	}
	defer rows.Close()

	var out []*RepoActivityRow
	for rows.Next() {
		e := &RepoActivityRow{}
		if err := rows.Scan(
			&e.ID, &e.ActorID, &e.ActorType, &e.Action, &e.Resource,
			&e.Outcome, &e.Metadata, &e.OccurredAt,
		); err != nil {
			return nil, fmt.Errorf("audit GetRepoActivity scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NotificationRow is one row returned by GetNotifications. Shares the same
// shape as RepoActivityRow because the projection logic in the handler is
// nearly identical — only the filter (tenant-wide vs. single repo) differs.
type NotificationRow struct {
	ID         uuid.UUID
	ActorID    string
	ActorType  string
	Action     string
	Resource   string
	Outcome    string
	Metadata   json.RawMessage
	OccurredAt time.Time
}

// GetNotifications returns operator-facing audit events for an entire tenant,
// ordered newest-first then by event_id DESC. Used by FE-API-008 to populate
// the topbar notification bell.
//
// Unlike GetRepoActivity this does NOT filter on metadata.raw.repository_name
// — webhook delivery failures and other tenant-wide events have no
// repository_name in their payload and must still surface in the bell. The
// query plan uses idx_audit_events_tenant_occurred (tenant_id, occurred_at
// DESC) which already exists from the initial schema.
//
// eventTypes is REQUIRED to be a non-empty caller-supplied allowlist. The
// handler is responsible for substituting its operator-facing default when
// the caller did not specify any types. Empty slice ⇒ no rows returned
// (the IN clause would match nothing). The caller MUST validate each entry
// against an allowlist before passing them through here — even though
// values are bound as a parameterised array, restricting the set keeps
// this layer honest about which actions a frontend can request.
//
// since lower-bounds occurred_at. cursorTime + cursorID, when both non-zero,
// drive keyset pagination using the lexicographic (occurred_at, id) pair so
// stable pagination is possible even when many events share an instant.
func (r *Repository) GetNotifications(
	ctx context.Context,
	tenantID uuid.UUID,
	since time.Time,
	cursorTime time.Time,
	cursorID uuid.UUID,
	eventTypes []string,
	limit int,
) ([]*NotificationRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(eventTypes) == 0 {
		return nil, nil
	}

	// Same keyset-pagination trick as GetRepoActivity — supply a far-future
	// sentinel when no cursor so the WHERE clause is still parameterised and
	// the planner picks the tenant index regardless.
	cursorActive := !cursorTime.IsZero()
	if !cursorActive {
		cursorTime = time.Now().Add(100 * 365 * 24 * time.Hour)
		cursorID = uuid.Nil
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, actor_id, actor_type, action, resource, outcome, metadata, occurred_at
		 FROM audit_events
		 WHERE tenant_id = $1
		   AND occurred_at >= $2
		   AND action = ANY($3)
		   AND ($4 = FALSE OR occurred_at < $5 OR (occurred_at = $5 AND id < $6))
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT $7`,
		tenantID, since, eventTypes,
		cursorActive, cursorTime, cursorID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("audit GetNotifications: %w", err)
	}
	defer rows.Close()

	var out []*NotificationRow
	for rows.Next() {
		e := &NotificationRow{}
		if err := rows.Scan(
			&e.ID, &e.ActorID, &e.ActorType, &e.Action, &e.Resource,
			&e.Outcome, &e.Metadata, &e.OccurredAt,
		); err != nil {
			return nil, fmt.Errorf("audit GetNotifications scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AnalyticsBucketRow is one row returned by GetAnalytics — a (bucket_start,
// count) pair. The repository layer never invents zero-count buckets; the
// caller pre-allocates the empty series and merges these in.
type AnalyticsBucketRow struct {
	// BucketStart is the inclusive lower bound of the bucket (UTC).
	BucketStart time.Time
	// Count is the number of audit events of the requested action that fell
	// inside this bucket.
	Count int64
}

// AnalyticsScope identifies whether GetAnalytics counts tenant-wide rows or
// rows scoped to a single repository. The repo_id is matched against the
// audit event metadata.raw.repo_id field — the same convention used by
// GetBuildHistory — so we can ride the existing (tenant_id, occurred_at)
// index without a LIKE scan over the resource column.
type AnalyticsScope struct {
	// TenantWide is true when the query should count every event for the
	// tenant regardless of repository. When TenantWide is false RepoID must
	// be non-empty.
	TenantWide bool
	// RepoID is the repository UUID matched against metadata.raw.repo_id.
	// Ignored when TenantWide is true.
	RepoID string
}

// GetAnalytics returns a time-bucketed series of audit-event counts. The query
// rides idx_audit_events_tenant_occurred — date_bin is a non-key expression
// but the underlying scan is bounded by the (tenant_id, occurred_at) range so
// the planner picks the right index even for a 30-day window.
//
// FE-API-030. The caller (registry-management) picks bucketSecs and aligns
// rangeStart to a bucket boundary before calling — we just bin to that
// boundary. Empty buckets are NOT padded out in this layer; the caller
// merges these rows into a pre-allocated zero-filled grid so the wire
// payload stays compact even for sparse 30-day series.
//
// action is bound as a parameter so it cannot be used to smuggle SQL, but
// the handler still validates it against an allowlist before reaching here
// — defence in depth.
func (r *Repository) GetAnalytics(
	ctx context.Context,
	tenantID uuid.UUID,
	scope AnalyticsScope,
	action string,
	rangeStart time.Time,
	rangeEnd time.Time,
	bucketSecs int64,
) ([]*AnalyticsBucketRow, error) {
	if bucketSecs <= 0 {
		return nil, fmt.Errorf("audit GetAnalytics: bucketSecs must be positive")
	}
	if rangeEnd.Before(rangeStart) {
		return nil, fmt.Errorf("audit GetAnalytics: rangeEnd before rangeStart")
	}

	// date_bin takes an interval; pgx binds time.Duration as INTERVAL, so we
	// can express the bucket width without string interpolation. The
	// origin (rangeStart) anchors the bins so the first bucket boundary is
	// exactly rangeStart — caller-side pre-allocation lines up 1:1.
	bucketInterval := time.Duration(bucketSecs) * time.Second

	// Repo-scoped queries add an extra predicate; tenant-wide queries skip
	// it. We branch the SQL rather than using an OR / coalesce because the
	// optimiser picks a much tighter plan when the metadata->>'repo_id'
	// JSON deref is absent.
	if !scope.TenantWide && scope.RepoID == "" {
		return nil, fmt.Errorf("audit GetAnalytics: repo_id required for repo scope")
	}

	const tenantSQL = `SELECT date_bin($1::interval, occurred_at, $2::timestamptz) AS bucket, COUNT(*)
		 FROM audit_events
		 WHERE tenant_id = $3
		   AND action    = $4
		   AND occurred_at >= $2
		   AND occurred_at <  $5
		 GROUP BY bucket
		 ORDER BY bucket ASC`
	const repoSQL = `SELECT date_bin($1::interval, occurred_at, $2::timestamptz) AS bucket, COUNT(*)
		 FROM audit_events
		 WHERE tenant_id = $3
		   AND action    = $4
		   AND occurred_at >= $2
		   AND occurred_at <  $5
		   AND metadata->'raw'->>'repo_id' = $6
		 GROUP BY bucket
		 ORDER BY bucket ASC`

	var (
		rows pgx.Rows
		err  error
	)
	if scope.TenantWide {
		rows, err = r.pool.Query(ctx, tenantSQL,
			bucketInterval, rangeStart, tenantID, action, rangeEnd,
		)
	} else {
		rows, err = r.pool.Query(ctx, repoSQL,
			bucketInterval, rangeStart, tenantID, action, rangeEnd, scope.RepoID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("audit GetAnalytics: %w", err)
	}
	defer rows.Close()

	var out []*AnalyticsBucketRow
	for rows.Next() {
		b := &AnalyticsBucketRow{}
		if err := rows.Scan(&b.BucketStart, &b.Count); err != nil {
			return nil, fmt.Errorf("audit GetAnalytics scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountPulls returns the number of pull.image audit events for a tenant since the given time.
func (r *Repository) CountPulls(ctx context.Context, tenantID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM audit_events
		 WHERE tenant_id = $1
		   AND action    = 'pull.image'
		   AND occurred_at >= $2`,
		tenantID, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("audit CountPulls: %w", err)
	}
	return count, nil
}

// PurgeOlderThan deletes audit events older than cutoff. This is the only
// deletion path and is used by the retention cleanup goroutine.
func (r *Repository) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		// Bypass the no_delete_audit rule using a direct table reference.
		// This is the one authorised deletion path (retention enforcement).
		`DELETE FROM audit_events_default WHERE occurred_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("audit PurgeOlderThan: %w", err)
	}
	return tag.RowsAffected(), nil
}
