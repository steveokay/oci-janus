package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FUT-019 Phase 3 — email notification channel persistence.
//
// Two tables back the feature (migration 20260707120000_email_channel):
//
//   email_transport_config  — one row per tenant. Holds the provider
//                             selection (resend|smtp), the from-identity,
//                             and the *sealed* provider secrets. Secret
//                             columns are BYTEA ciphertext — this layer
//                             is SQL-only and never encrypts/decrypts.
//                             The handler (Task 7) seals/opens the bytes
//                             with the email KEK, exactly as audit_export.go
//                             keeps resolveSecret/openSecret out of the repo.
//   email_deliveries        — per-send log AND send queue. The dispatcher
//                             enqueues one pending row per opted-in
//                             recipient; the send loop claims pending rows
//                             (lease via next_attempt_at), sends, then marks
//                             each row sent or failed.

// EmailTransportConfig mirrors an email_transport_config row. Secret columns
// (ResendAPIKeyEnc, SMTPPasswordEnc) hold ciphertext (BYTEA) — the handler
// seals/opens them with the email KEK, so nil means "no secret stored yet".
type EmailTransportConfig struct {
	TenantID        uuid.UUID
	Provider        string
	Enabled         bool
	FromAddress     string
	FromName        string
	ResendAPIKeyEnc []byte
	SMTPHost        string
	SMTPPort        int
	SMTPUsername    string
	SMTPPasswordEnc []byte
	SMTPTLSMode     string
	KEKVersion      int16
	LastTestAt      *time.Time
	LastTestOK      *bool
	LastTestError   string
	UpdatedAt       time.Time
	UpdatedBy       *uuid.UUID
}

// EmailDelivery mirrors an email_deliveries row. It is both the audit log of
// a send and the unit of work drained by the send loop.
type EmailDelivery struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	UserID            uuid.UUID
	ToAddress         string
	Category          string
	Subject           string
	BodySummary       string
	Link              string
	SourceScheduledID uuid.UUID
	Status            string
	Attempts          int
	NextAttemptAt     time.Time
	LastError         string
	Provider          string
	CreatedAt         time.Time
	SentAt            *time.Time
}

// ── email_transport_config ───────────────────────────────────────────

// GetEmailTransportConfig returns the transport config for a tenant, or
// (nil, nil) when the tenant has never saved one. Nullable TEXT/INT columns
// are COALESCEd to their zero values so the struct fields stay non-pointer;
// the BYTEA secret columns and the *TestAt/*TestOK/UpdatedBy columns are
// left nullable and scan into pointers/slices.
func (r *Repository) GetEmailTransportConfig(
	ctx context.Context,
	tenantID uuid.UUID,
) (*EmailTransportConfig, error) {
	const q = `
		SELECT tenant_id, provider, enabled,
		       COALESCE(from_address, ''), COALESCE(from_name, ''),
		       resend_api_key_enc,
		       COALESCE(smtp_host, ''), COALESCE(smtp_port, 0),
		       COALESCE(smtp_username, ''), smtp_password_enc,
		       smtp_tls_mode, kek_version,
		       last_test_at, last_test_ok, COALESCE(last_test_error, ''),
		       updated_at, updated_by
		  FROM email_transport_config
		 WHERE tenant_id = $1`
	var c EmailTransportConfig
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&c.TenantID, &c.Provider, &c.Enabled,
		&c.FromAddress, &c.FromName,
		&c.ResendAPIKeyEnc,
		&c.SMTPHost, &c.SMTPPort,
		&c.SMTPUsername, &c.SMTPPasswordEnc,
		&c.SMTPTLSMode, &c.KEKVersion,
		&c.LastTestAt, &c.LastTestOK, &c.LastTestError,
		&c.UpdatedAt, &c.UpdatedBy,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			// No config saved yet — not an error for the caller.
			return nil, nil
		}
		return nil, fmt.Errorf("get email transport config: %w", err)
	}
	return &c, nil
}

// UpsertEmailTransportConfig inserts-or-replaces the whole config row for a
// tenant. Every column except the tenant_id primary key is overwritten from
// the supplied struct (secrets included — pass the previously-stored
// ciphertext through unchanged when the caller isn't rotating a secret).
// updated_at is stamped to now() on both the insert and the conflict path.
func (r *Repository) UpsertEmailTransportConfig(
	ctx context.Context,
	cfg EmailTransportConfig,
) error {
	const q = `
		INSERT INTO email_transport_config
			(tenant_id, provider, enabled, from_address, from_name,
			 resend_api_key_enc, smtp_host, smtp_port, smtp_username,
			 smtp_password_enc, smtp_tls_mode, kek_version,
			 last_test_at, last_test_ok, last_test_error, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			provider           = EXCLUDED.provider,
			enabled            = EXCLUDED.enabled,
			from_address       = EXCLUDED.from_address,
			from_name          = EXCLUDED.from_name,
			resend_api_key_enc = EXCLUDED.resend_api_key_enc,
			smtp_host          = EXCLUDED.smtp_host,
			smtp_port          = EXCLUDED.smtp_port,
			smtp_username      = EXCLUDED.smtp_username,
			smtp_password_enc  = EXCLUDED.smtp_password_enc,
			smtp_tls_mode      = EXCLUDED.smtp_tls_mode,
			kek_version        = EXCLUDED.kek_version,
			last_test_at       = EXCLUDED.last_test_at,
			last_test_ok       = EXCLUDED.last_test_ok,
			last_test_error    = EXCLUDED.last_test_error,
			updated_by         = EXCLUDED.updated_by,
			updated_at         = now()`
	// Normalise empty nullable strings to NULL so the DB stores clean NULLs
	// rather than empty text (keeps the "unset" state unambiguous).
	_, err := r.pool.Exec(ctx, q,
		cfg.TenantID, cfg.Provider, cfg.Enabled,
		nullString(cfg.FromAddress), nullString(cfg.FromName),
		cfg.ResendAPIKeyEnc, nullString(cfg.SMTPHost), nullInt(cfg.SMTPPort),
		nullString(cfg.SMTPUsername), cfg.SMTPPasswordEnc,
		cfg.SMTPTLSMode, cfg.KEKVersion,
		cfg.LastTestAt, cfg.LastTestOK, nullString(cfg.LastTestError),
		cfg.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("upsert email transport config: %w", err)
	}
	return nil
}

// UpdateEmailTestResult records the outcome of a "send test email" probe
// without touching the rest of the config. A missing config row (tenant never
// saved one) is a no-op.
func (r *Repository) UpdateEmailTestResult(
	ctx context.Context,
	tenantID uuid.UUID,
	ok bool,
	errMsg string,
) error {
	const q = `
		UPDATE email_transport_config
		   SET last_test_at    = now(),
		       last_test_ok    = $2,
		       last_test_error = $3
		 WHERE tenant_id = $1`
	_, err := r.pool.Exec(ctx, q, tenantID, ok, nullString(errMsg))
	if err != nil {
		return fmt.Errorf("update email test result: %w", err)
	}
	return nil
}

// ── email_deliveries ─────────────────────────────────────────────────

// EnqueueEmailDelivery inserts one pending delivery row. The unique
// (source_scheduled_id, user_id) constraint makes the fan-out idempotent: if
// the dispatcher retries after a crash, the duplicate insert lands in the
// ON CONFLICT DO NOTHING path so a recipient is never emailed twice for the
// same scheduled notification.
func (r *Repository) EnqueueEmailDelivery(ctx context.Context, d EmailDelivery) error {
	const q = `
		INSERT INTO email_deliveries
			(tenant_id, user_id, to_address, category, subject,
			 body_summary, link, source_scheduled_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (source_scheduled_id, user_id) DO NOTHING`
	_, err := r.pool.Exec(ctx, q,
		d.TenantID, d.UserID, d.ToAddress, d.Category, d.Subject,
		d.BodySummary, nullString(d.Link), d.SourceScheduledID,
	)
	if err != nil {
		return fmt.Errorf("enqueue email delivery: %w", err)
	}
	return nil
}

// ListEmailRecipients returns the user ids that have opted in to the email
// channel for a (tenant, category) pair. Only rows with email_enabled=true
// are returned — the default (no preference row) is email OFF, so a user must
// explicitly opt in to receive email for a category.
func (r *Repository) ListEmailRecipients(
	ctx context.Context,
	tenantID uuid.UUID,
	category string,
) ([]uuid.UUID, error) {
	const q = `
		SELECT user_id
		  FROM user_notification_preferences
		 WHERE tenant_id = $1
		   AND category  = $2
		   AND email_enabled = true`
	rows, err := r.pool.Query(ctx, q, tenantID, category)
	if err != nil {
		return nil, fmt.Errorf("list email recipients: %w", err)
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0, 16)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ClaimPendingEmailDeliveries atomically leases up to `limit` pending rows
// that are due now-or-earlier. Unlike the scheduled-notification claim it does
// NOT flip the row to an intermediate status — instead it pushes
// next_attempt_at one minute into the future so a crashed sender's rows become
// claimable again after the lease expires (the send loop calls MarkEmailSent /
// MarkEmailFailed to close each row). FOR UPDATE SKIP LOCKED lets multiple
// sender workers drain the queue without contending on the same rows.
func (r *Repository) ClaimPendingEmailDeliveries(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]*EmailDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `
		WITH claimed AS (
			SELECT id FROM email_deliveries
			 WHERE status = 'pending' AND next_attempt_at <= $1
			 ORDER BY next_attempt_at
			   FOR UPDATE SKIP LOCKED
			 LIMIT $2
		)
		UPDATE email_deliveries d
		   SET next_attempt_at = now() + interval '1 minute'
		  FROM claimed
		 WHERE d.id = claimed.id
		RETURNING d.id, d.tenant_id, d.user_id, d.to_address, d.category, d.subject,
		          d.body_summary, COALESCE(d.link, ''), d.source_scheduled_id, d.status,
		          d.attempts, d.next_attempt_at, COALESCE(d.last_error, ''),
		          COALESCE(d.provider, ''), d.created_at, d.sent_at`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim pending email deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]*EmailDelivery, 0, limit)
	for rows.Next() {
		var d EmailDelivery
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.UserID, &d.ToAddress, &d.Category, &d.Subject,
			&d.BodySummary, &d.Link, &d.SourceScheduledID, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError,
			&d.Provider, &d.CreatedAt, &d.SentAt,
		); err != nil {
			return nil, fmt.Errorf("scan claimed email delivery: %w", err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// MarkEmailSent flips a delivery to sent, stamps sent_at, records which
// provider delivered it, and clears any prior error. Called after the send
// loop successfully hands the message to Resend/SMTP.
func (r *Repository) MarkEmailSent(ctx context.Context, id uuid.UUID, provider string) error {
	const q = `
		UPDATE email_deliveries
		   SET status     = 'sent',
		       sent_at    = now(),
		       provider   = $2,
		       last_error = NULL
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, provider)
	if err != nil {
		return fmt.Errorf("mark email sent: %w", err)
	}
	return nil
}

// MarkEmailFailed records a send failure. The caller owns the retry policy: it
// passes the bumped attempt count, the computed next_attempt_at (backoff), and
// `failed` — true once the retry budget is exhausted (row parked at 'failed'),
// false to leave the row 'pending' for another attempt at next_attempt_at.
func (r *Repository) MarkEmailFailed(
	ctx context.Context,
	id uuid.UUID,
	attempts int,
	nextAttempt time.Time,
	failed bool,
	errMsg string,
) error {
	const q = `
		UPDATE email_deliveries
		   SET attempts        = $2,
		       next_attempt_at = $3,
		       status          = CASE WHEN $4 THEN 'failed' ELSE 'pending' END,
		       last_error      = $5
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, attempts, nextAttempt, failed, nullString(errMsg))
	if err != nil {
		return fmt.Errorf("mark email failed: %w", err)
	}
	return nil
}

// ListEmailDeliveries returns a user's recent email deliveries, newest first,
// scoped to a single tenant. Backs the FE "email delivery history" panel.
func (r *Repository) ListEmailDeliveries(
	ctx context.Context,
	tenantID uuid.UUID,
	userID uuid.UUID,
	limit int,
) ([]*EmailDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT id, tenant_id, user_id, to_address, category, subject,
		       body_summary, COALESCE(link, ''), source_scheduled_id, status,
		       attempts, next_attempt_at, COALESCE(last_error, ''),
		       COALESCE(provider, ''), created_at, sent_at
		  FROM email_deliveries
		 WHERE tenant_id = $1
		   AND user_id   = $2
		 ORDER BY created_at DESC
		 LIMIT $3`
	rows, err := r.pool.Query(ctx, q, tenantID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list email deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]*EmailDelivery, 0, limit)
	for rows.Next() {
		var d EmailDelivery
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.UserID, &d.ToAddress, &d.Category, &d.Subject,
			&d.BodySummary, &d.Link, &d.SourceScheduledID, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError,
			&d.Provider, &d.CreatedAt, &d.SentAt,
		); err != nil {
			return nil, fmt.Errorf("scan email delivery: %w", err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// nullString maps "" → nil (SQL NULL) so nullable TEXT columns stay NULL
// instead of storing empty strings. Non-empty values pass through unchanged.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt maps 0 → nil (SQL NULL) for nullable INT columns (e.g. smtp_port,
// which is unset until an SMTP config is saved).
func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}
