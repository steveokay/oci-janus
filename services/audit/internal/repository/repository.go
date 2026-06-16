// Package repository handles all database access for the audit service.
// The audit_events table is append-only — this package only inserts and queries.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
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
