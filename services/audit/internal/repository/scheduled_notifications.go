package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FUT-019 Phase 2 — scheduled-notification persistence.
//
// Two tables back the feature:
//
//   scheduled_notifications        — work queue. Scheduler inserts a
//                                    row when a category is due, the
//                                    dispatcher claims it via FOR
//                                    UPDATE SKIP LOCKED, renders one
//                                    notification per recipient, marks
//                                    the row delivered.
//   user_notification_preferences  — per-user opt-in matrix. Missing
//                                    rows mean "use defaults" (bell on,
//                                    email off, webhook off).

// ScheduledNotificationStatus mirrors the SQL CHECK constraint values
// (migration 20260626000001). The state machine is pending →
// in_progress → delivered (or failed after retry exhaustion).
type ScheduledNotificationStatus string

const (
	ScheduledStatusPending    ScheduledNotificationStatus = "pending"
	ScheduledStatusInProgress ScheduledNotificationStatus = "in_progress"
	ScheduledStatusDelivered  ScheduledNotificationStatus = "delivered"
	ScheduledStatusFailed     ScheduledNotificationStatus = "failed"
)

// ScheduledNotification mirrors the scheduled_notifications row. The
// payload is a JSON envelope shaped by each category — see
// services/audit/internal/scheduler/categories.go for the per-category
// shapes.
type ScheduledNotification struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Category    string
	DueAt       time.Time
	Payload     json.RawMessage
	Status      ScheduledNotificationStatus
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeliveredAt *time.Time
}

// ScheduleNotification idempotently inserts a (tenant_id, category,
// due_at) row. The unique index on (tenant_id, category, due_at) makes
// this safe to call from a scheduler that retries after a crash — a
// duplicate insert lands in the ON CONFLICT no-op path and returns
// nil + the already-scheduled status.
//
// Returns true when a new row was created, false when the conflict
// path swallowed the insert. Callers can use the boolean to log
// "scheduled" vs "already scheduled".
func (r *Repository) ScheduleNotification(
	ctx context.Context,
	tenantID uuid.UUID,
	category string,
	dueAt time.Time,
	payload json.RawMessage,
) (bool, error) {
	if payload == nil {
		payload = json.RawMessage("{}")
	}
	const q = `
		INSERT INTO scheduled_notifications
			(tenant_id, category, due_at, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, category, due_at) DO NOTHING
		RETURNING id`
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, q, tenantID, category, dueAt, payload).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Conflict path — row already exists.
			return false, nil
		}
		return false, fmt.Errorf("schedule notification: %w", err)
	}
	return true, nil
}

// ClaimDueNotifications atomically pulls up to `limit` pending rows
// that are due now-or-earlier and flips them to in_progress. Uses
// FOR UPDATE SKIP LOCKED so multiple dispatcher workers (running in
// parallel pods) can drain the queue without contending on the same
// rows.
//
// The caller renders + writes the notification_events rows + then
// calls MarkDelivered or MarkFailed to close the row's state.
func (r *Repository) ClaimDueNotifications(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]*ScheduledNotification, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `
		WITH claimed AS (
			SELECT id
			  FROM scheduled_notifications
			 WHERE status = 'pending'
			   AND due_at <= $1
			 ORDER BY due_at
			   FOR UPDATE SKIP LOCKED
			 LIMIT $2
		)
		UPDATE scheduled_notifications sn
		   SET status     = 'in_progress',
		       attempts   = attempts + 1,
		       updated_at = now()
		  FROM claimed
		 WHERE sn.id = claimed.id
		RETURNING sn.id, sn.tenant_id, sn.category, sn.due_at, sn.payload,
		          sn.status, sn.attempts, COALESCE(sn.last_error, ''),
		          sn.created_at, sn.updated_at, sn.delivered_at`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due notifications: %w", err)
	}
	defer rows.Close()
	out := make([]*ScheduledNotification, 0, limit)
	for rows.Next() {
		var n ScheduledNotification
		if err := rows.Scan(
			&n.ID, &n.TenantID, &n.Category, &n.DueAt, &n.Payload,
			&n.Status, &n.Attempts, &n.LastError,
			&n.CreatedAt, &n.UpdatedAt, &n.DeliveredAt,
		); err != nil {
			return nil, fmt.Errorf("scan claimed notification: %w", err)
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

// MarkDelivered flips a row to delivered + sets delivered_at. Called
// after the dispatcher successfully wrote the notification_events
// fan-out for this row.
func (r *Repository) MarkDelivered(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE scheduled_notifications
		   SET status       = 'delivered',
		       delivered_at = now(),
		       updated_at   = now(),
		       last_error   = NULL
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

// MarkFailed records a delivery failure. After 3 attempts the dispatcher
// flips the row to status='failed' and stops retrying; before that it
// stays in 'in_progress' (the attempts counter was bumped by the claim
// query) and the next dispatcher tick will pick it back up by virtue
// of in_progress → pending repaint below.
//
// We don't auto-revert in_progress → pending here because we want the
// retry budget to be visible: a row stuck at attempts=3 status=failed
// is debuggable; an in_progress row pinging the queue is not. The
// dispatcher's RevertStuckInProgress sweep below handles the
// crash-during-render case.
func (r *Repository) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	const q = `
		UPDATE scheduled_notifications
		   SET status     = CASE WHEN attempts >= 3 THEN 'failed' ELSE 'pending' END,
		       last_error = $2,
		       updated_at = now()
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, errMsg)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// RevertStuckInProgress flips rows that have been in_progress longer
// than `maxAge` back to pending. Catches the "dispatcher crashed mid-
// render" case where a row got claimed but never marked. Runs as a
// periodic sweep alongside the dispatcher loop.
func (r *Repository) RevertStuckInProgress(ctx context.Context, maxAge time.Duration) (int64, error) {
	const q = `
		UPDATE scheduled_notifications
		   SET status = 'pending',
		       updated_at = now()
		 WHERE status = 'in_progress'
		   AND updated_at < now() - ($1::interval)`
	tag, err := r.pool.Exec(ctx, q, maxAge)
	if err != nil {
		return 0, fmt.Errorf("revert stuck in_progress: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListActiveTenants returns the distinct set of tenant_ids that have
// emitted audit_events in the last `window`. Used by the scheduler to
// decide which tenants get scheduled notifications — there's no
// point scheduling a scanner_freshness reminder for a tenant that
// hasn't pushed anything in months.
func (r *Repository) ListActiveTenants(ctx context.Context, window time.Duration) ([]uuid.UUID, error) {
	const q = `
		SELECT DISTINCT tenant_id
		  FROM audit_events
		 WHERE occurred_at > now() - ($1::interval)`
	rows, err := r.pool.Query(ctx, q, window)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0, 32)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// LastScheduledAt returns the most recent scheduled_at for a
// (tenant_id, category) pair. Used by the scheduler to decide whether
// it's time to schedule the next occurrence. NULL → never scheduled,
// returned as a zero time.Time.
func (r *Repository) LastScheduledAt(
	ctx context.Context,
	tenantID uuid.UUID,
	category string,
) (time.Time, error) {
	const q = `
		SELECT COALESCE(MAX(due_at), '1970-01-01'::timestamptz)
		  FROM scheduled_notifications
		 WHERE tenant_id = $1
		   AND category  = $2`
	var t time.Time
	if err := r.pool.QueryRow(ctx, q, tenantID, category).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("last scheduled at: %w", err)
	}
	return t, nil
}

// ── user_notification_preferences ────────────────────────────────────

// NotificationPreference mirrors one row of user_notification_preferences.
type NotificationPreference struct {
	UserID         uuid.UUID
	TenantID       uuid.UUID
	Category       string
	BellEnabled    bool
	EmailEnabled   bool
	WebhookEnabled bool
	UpdatedAt      time.Time
}

// GetUserPreferences returns the preferences a user has explicitly
// set. Categories with no row are NOT included — the caller computes
// the default (bell on, email off, webhook off) for unset categories
// itself so the wire shape stays narrow.
func (r *Repository) GetUserPreferences(
	ctx context.Context,
	userID uuid.UUID,
) ([]*NotificationPreference, error) {
	const q = `
		SELECT user_id, tenant_id, category,
		       bell_enabled, email_enabled, webhook_enabled, updated_at
		  FROM user_notification_preferences
		 WHERE user_id = $1`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("get user preferences: %w", err)
	}
	defer rows.Close()
	out := make([]*NotificationPreference, 0, 8)
	for rows.Next() {
		var p NotificationPreference
		if err := rows.Scan(
			&p.UserID, &p.TenantID, &p.Category,
			&p.BellEnabled, &p.EmailEnabled, &p.WebhookEnabled, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan preference: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// UpsertUserPreference inserts-or-updates one (user_id, category) row.
// Called from the PATCH /notification-preferences route — one PATCH
// can touch multiple categories in sequence; the caller iterates and
// upserts each.
func (r *Repository) UpsertUserPreference(
	ctx context.Context,
	p NotificationPreference,
) error {
	const q = `
		INSERT INTO user_notification_preferences
			(user_id, tenant_id, category,
			 bell_enabled, email_enabled, webhook_enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, category) DO UPDATE
		  SET tenant_id       = EXCLUDED.tenant_id,
		      bell_enabled    = EXCLUDED.bell_enabled,
		      email_enabled   = EXCLUDED.email_enabled,
		      webhook_enabled = EXCLUDED.webhook_enabled,
		      updated_at      = now()`
	_, err := r.pool.Exec(ctx, q,
		p.UserID, p.TenantID, p.Category,
		p.BellEnabled, p.EmailEnabled, p.WebhookEnabled,
	)
	if err != nil {
		return fmt.Errorf("upsert user preference: %w", err)
	}
	return nil
}

// IsBellEnabledForCategory returns whether the bell channel is on for
// (user_id, category). Defaults to TRUE when no row exists.
//
// Used by the notifications-bell read path to filter out muted
// categories. Phase 2 ships bell-only; email + webhook channels are
// Phase 3+.
func (r *Repository) IsBellEnabledForCategory(
	ctx context.Context,
	userID uuid.UUID,
	category string,
) (bool, error) {
	const q = `
		SELECT bell_enabled
		  FROM user_notification_preferences
		 WHERE user_id = $1 AND category = $2`
	var enabled bool
	err := r.pool.QueryRow(ctx, q, userID, category).Scan(&enabled)
	if err != nil {
		if err == pgx.ErrNoRows {
			return true, nil // default: bell on
		}
		return false, fmt.Errorf("is bell enabled: %w", err)
	}
	return enabled, nil
}
