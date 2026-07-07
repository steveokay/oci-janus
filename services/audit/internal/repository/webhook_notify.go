package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FUT-019 Webhook channel persistence. Two tables back the feature
// (migration 20260708120000_webhook_channel):
//
//   notification_webhook_config      — one row per tenant. The admin org
//                                      webhook URL + sealed HMAC secret +
//                                      the tenant-level enabled_categories set.
//                                      secret_enc is BYTEA ciphertext — this
//                                      layer is SQL-only; the handler seals/
//                                      opens it with the webhook KEK.
//   notification_webhook_deliveries  — per-send log AND send queue. The
//                                      dispatcher enqueues one pending row per
//                                      scheduled notification; the send loop
//                                      claims (lease via next_attempt_at),
//                                      posts, then marks delivered or failed.

// NotificationWebhookConfig mirrors a notification_webhook_config row. SecretEnc
// holds ciphertext (BYTEA); the handler seals/opens it with the webhook KEK, so
// nil means "no secret stored yet".
type NotificationWebhookConfig struct {
	TenantID          uuid.UUID
	URL               string
	SecretEnc         []byte
	Enabled           bool
	EnabledCategories []string
	KEKVersion        int16
	LastTestAt        *time.Time
	LastTestOK        *bool
	LastTestError     string
	UpdatedAt         time.Time
	UpdatedBy         *uuid.UUID
}

// WebhookDelivery mirrors a notification_webhook_deliveries row. It is both the
// audit log of a send and the unit of work drained by the send loop.
type WebhookDelivery struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Category          string
	Subject           string
	BodySummary       string
	Link              string
	SourceScheduledID uuid.UUID
	Status            string
	Attempts          int
	NextAttemptAt     time.Time
	LastError         string
	ResponseStatus    int
	CreatedAt         time.Time
	DeliveredAt       *time.Time
}

// ── notification_webhook_config ──────────────────────────────────────

// GetNotificationWebhookConfig returns the webhook config for a tenant, or
// (nil, nil) when the tenant has never saved one.
func (r *Repository) GetNotificationWebhookConfig(
	ctx context.Context,
	tenantID uuid.UUID,
) (*NotificationWebhookConfig, error) {
	const q = `
		SELECT tenant_id, COALESCE(url, ''), secret_enc, enabled,
		       enabled_categories, kek_version,
		       last_test_at, last_test_ok, COALESCE(last_test_error, ''),
		       updated_at, updated_by
		  FROM notification_webhook_config
		 WHERE tenant_id = $1`
	var c NotificationWebhookConfig
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&c.TenantID, &c.URL, &c.SecretEnc, &c.Enabled,
		&c.EnabledCategories, &c.KEKVersion,
		&c.LastTestAt, &c.LastTestOK, &c.LastTestError,
		&c.UpdatedAt, &c.UpdatedBy,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get notification webhook config: %w", err)
	}
	return &c, nil
}

// UpsertNotificationWebhookConfig inserts-or-replaces the whole config row.
// Pass the previously-stored ciphertext through unchanged when not rotating the
// secret. updated_at is stamped to now() on both paths.
func (r *Repository) UpsertNotificationWebhookConfig(
	ctx context.Context,
	cfg NotificationWebhookConfig,
) error {
	// enabled_categories is NOT NULL DEFAULT '{}'; a nil slice must land as an
	// empty array, not NULL, so normalise here.
	cats := cfg.EnabledCategories
	if cats == nil {
		cats = []string{}
	}
	const q = `
		INSERT INTO notification_webhook_config
			(tenant_id, url, secret_enc, enabled, enabled_categories,
			 kek_version, last_test_at, last_test_ok, last_test_error,
			 updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			url                = EXCLUDED.url,
			secret_enc         = EXCLUDED.secret_enc,
			enabled            = EXCLUDED.enabled,
			enabled_categories = EXCLUDED.enabled_categories,
			kek_version        = EXCLUDED.kek_version,
			last_test_at       = EXCLUDED.last_test_at,
			last_test_ok       = EXCLUDED.last_test_ok,
			last_test_error    = EXCLUDED.last_test_error,
			updated_by         = EXCLUDED.updated_by,
			updated_at         = now()`
	_, err := r.pool.Exec(ctx, q,
		cfg.TenantID, nullString(cfg.URL), cfg.SecretEnc, cfg.Enabled, cats,
		cfg.KEKVersion, cfg.LastTestAt, cfg.LastTestOK, nullString(cfg.LastTestError),
		cfg.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("upsert notification webhook config: %w", err)
	}
	return nil
}

// UpdateWebhookTestResult records the outcome of a "send test" probe without
// touching the rest of the config. A missing config row is a no-op.
func (r *Repository) UpdateWebhookTestResult(
	ctx context.Context,
	tenantID uuid.UUID,
	ok bool,
	errMsg string,
) error {
	const q = `
		UPDATE notification_webhook_config
		   SET last_test_at    = now(),
		       last_test_ok    = $2,
		       last_test_error = $3
		 WHERE tenant_id = $1`
	_, err := r.pool.Exec(ctx, q, tenantID, ok, nullString(errMsg))
	if err != nil {
		return fmt.Errorf("update webhook test result: %w", err)
	}
	return nil
}

// ── notification_webhook_deliveries ──────────────────────────────────

// EnqueueWebhookDelivery inserts one pending delivery row. The unique
// (source_scheduled_id) constraint makes the fan-out idempotent: a dispatcher
// retry after a crash lands in ON CONFLICT DO NOTHING, so the org webhook is
// never posted twice for the same scheduled notification.
func (r *Repository) EnqueueWebhookDelivery(ctx context.Context, d WebhookDelivery) error {
	const q = `
		INSERT INTO notification_webhook_deliveries
			(tenant_id, category, subject, body_summary, link, source_scheduled_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source_scheduled_id) DO NOTHING`
	_, err := r.pool.Exec(ctx, q,
		d.TenantID, d.Category, d.Subject, d.BodySummary, nullString(d.Link), d.SourceScheduledID,
	)
	if err != nil {
		return fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	return nil
}

// ClaimPendingWebhookDeliveries atomically leases up to `limit` pending rows due
// now-or-earlier, pushing next_attempt_at one minute out so a crashed sender's
// rows become claimable again after the lease expires. FOR UPDATE SKIP LOCKED
// lets workers drain without contending. Mirrors ClaimPendingEmailDeliveries.
func (r *Repository) ClaimPendingWebhookDeliveries(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]*WebhookDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `
		WITH claimed AS (
			SELECT id FROM notification_webhook_deliveries
			 WHERE status = 'pending' AND next_attempt_at <= $1
			 ORDER BY next_attempt_at
			   FOR UPDATE SKIP LOCKED
			 LIMIT $2
		)
		UPDATE notification_webhook_deliveries d
		   SET next_attempt_at = now() + interval '1 minute'
		  FROM claimed
		 WHERE d.id = claimed.id
		RETURNING d.id, d.tenant_id, d.category, d.subject, d.body_summary,
		          COALESCE(d.link, ''), d.source_scheduled_id, d.status,
		          d.attempts, d.next_attempt_at, COALESCE(d.last_error, ''),
		          COALESCE(d.response_status, 0), d.created_at, d.delivered_at`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim pending webhook deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]*WebhookDelivery, 0, limit)
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.Category, &d.Subject, &d.BodySummary,
			&d.Link, &d.SourceScheduledID, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError,
			&d.ResponseStatus, &d.CreatedAt, &d.DeliveredAt,
		); err != nil {
			return nil, fmt.Errorf("scan claimed webhook delivery: %w", err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// MarkWebhookDelivered flips a delivery to delivered, stamps delivered_at,
// records the HTTP status, and clears any prior error.
func (r *Repository) MarkWebhookDelivered(ctx context.Context, id uuid.UUID, responseStatus int) error {
	const q = `
		UPDATE notification_webhook_deliveries
		   SET status          = 'delivered',
		       delivered_at    = now(),
		       response_status = $2,
		       last_error      = NULL
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, responseStatus)
	if err != nil {
		return fmt.Errorf("mark webhook delivered: %w", err)
	}
	return nil
}

// MarkWebhookFailed records a send failure. The caller owns the retry policy:
// it passes the bumped attempt count, the computed next_attempt_at (backoff),
// `failed` (true once the budget is exhausted → row parked at 'failed'), the
// last HTTP status (0 when no response), and the redacted error.
func (r *Repository) MarkWebhookFailed(
	ctx context.Context,
	id uuid.UUID,
	attempts int,
	nextAttempt time.Time,
	failed bool,
	responseStatus int,
	errMsg string,
) error {
	const q = `
		UPDATE notification_webhook_deliveries
		   SET attempts        = $2,
		       next_attempt_at = $3,
		       status          = CASE WHEN $4 THEN 'failed' ELSE 'pending' END,
		       response_status = $5,
		       last_error      = $6
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, attempts, nextAttempt, failed, nullResponseStatus(responseStatus), nullString(errMsg))
	if err != nil {
		return fmt.Errorf("mark webhook failed: %w", err)
	}
	return nil
}

// nullResponseStatus maps 0 → nil (SQL NULL) for the nullable response_status
// column so "no HTTP response received" stays distinct from a real status 0.
func nullResponseStatus(code int) any {
	if code == 0 {
		return nil
	}
	return code
}
