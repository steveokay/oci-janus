package repository

// audit_export.go — futures.md Tier 1 #4 (audit log streaming to SIEM).
//
// Per-tenant export-destination CRUD + observability counters. The
// table is documented in migration `20260623100000_audit_export_configs.sql`.
// `hmac_secret` + `bearer_token` are persisted as AES-256-GCM ciphertext
// (encryption happens in services/audit/internal/service); this layer
// passes them through as opaque BYTEA.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuditExportConfig mirrors the audit_export_configs row. The encrypted
// columns are []byte so the service layer can apply Seal / Open without
// the repository ever seeing plaintext material. event_filters is
// JSONB on the database side; we hand the raw bytes off and let the
// service layer Unmarshal — same contract as audit_events.metadata.
type AuditExportConfig struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Enabled       bool
	Format        string // "syslog_rfc5424" | "cef" | "webhook"
	TargetURL     string
	HMACSecret    []byte // encrypted; nil when unset
	BearerToken   []byte // encrypted; nil when unset
	EventFilters  json.RawMessage
	LastSuccessAt *time.Time
	LastAttemptAt *time.Time
	LastError     string
	DLXDepth      int32
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ErrExportConfigNotFound is returned by GetAuditExportConfig when the
// tenant has never configured a streaming destination. Distinct error
// so the gRPC handler can surface NotFound rather than Internal.
var ErrExportConfigNotFound = errors.New("audit export config not found")

const auditExportColumns = `id, tenant_id, enabled, format, target_url,
		hmac_secret, bearer_token, event_filters,
		last_success_at, last_attempt_at, last_error, dlx_depth,
		created_by, created_at, updated_at`

// GetAuditExportConfig returns the streaming destination for a tenant.
// Returns ErrExportConfigNotFound when the tenant has no row yet — the
// caller (gRPC handler / exporter consumer) treats "no config" and
// "disabled" identically: skip streaming for this tenant.
func (r *Repository) GetAuditExportConfig(ctx context.Context, tenantID uuid.UUID) (*AuditExportConfig, error) {
	const q = `SELECT ` + auditExportColumns + `
		FROM audit_export_configs
		WHERE tenant_id = $1`
	row := r.pool.QueryRow(ctx, q, tenantID)
	out, err := scanAuditExportConfig(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrExportConfigNotFound
		}
		return nil, fmt.Errorf("get audit export config: %w", err)
	}
	return out, nil
}

// UpsertAuditExportConfig inserts or updates the per-tenant destination.
// The ON CONFLICT clause on (tenant_id) uses the UNIQUE constraint from
// migration 20260623100000 so the operator can PUT the same config
// repeatedly without churn — the updated_at trigger handles the
// timestamp. The encrypted-secret columns are written verbatim from
// the caller; the service layer is responsible for applying Seal()
// before calling here.
func (r *Repository) UpsertAuditExportConfig(ctx context.Context, cfg *AuditExportConfig) (*AuditExportConfig, error) {
	const q = `
		INSERT INTO audit_export_configs (
			tenant_id, enabled, format, target_url,
			hmac_secret, bearer_token, event_filters, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id) DO UPDATE SET
			enabled       = EXCLUDED.enabled,
			format        = EXCLUDED.format,
			target_url    = EXCLUDED.target_url,
			hmac_secret   = EXCLUDED.hmac_secret,
			bearer_token  = EXCLUDED.bearer_token,
			event_filters = EXCLUDED.event_filters,
			updated_at    = now()
		RETURNING ` + auditExportColumns
	row := r.pool.QueryRow(ctx, q,
		cfg.TenantID, cfg.Enabled, cfg.Format, cfg.TargetURL,
		cfg.HMACSecret, cfg.BearerToken, cfg.EventFilters, cfg.CreatedBy,
	)
	out, err := scanAuditExportConfig(row)
	if err != nil {
		return nil, fmt.Errorf("upsert audit export config: %w", err)
	}
	return out, nil
}

// DeleteAuditExportConfig clears the streaming destination. Idempotent
// — deleting a non-existent row is success (treats "delete what isn't
// there" as "the desired end-state is already achieved"). The
// audit_export consumer drops events for tenants with no row, so the
// stream stops on the next event after this returns.
func (r *Repository) DeleteAuditExportConfig(ctx context.Context, tenantID uuid.UUID) error {
	const q = `DELETE FROM audit_export_configs WHERE tenant_id = $1`
	if _, err := r.pool.Exec(ctx, q, tenantID); err != nil {
		return fmt.Errorf("delete audit export config: %w", err)
	}
	return nil
}

// TouchAuditExportSuccess records a successful delivery — bumps the
// last_success_at + last_attempt_at columns + clears last_error. Called
// from the exporter consumer's happy path. No row update if the tenant
// has no config (defensive — the consumer should not be running for
// such tenants in the first place).
func (r *Repository) TouchAuditExportSuccess(ctx context.Context, tenantID uuid.UUID) error {
	const q = `UPDATE audit_export_configs
		SET last_success_at = now(),
		    last_attempt_at = now(),
		    last_error      = ''
		WHERE tenant_id = $1`
	if _, err := r.pool.Exec(ctx, q, tenantID); err != nil {
		return fmt.Errorf("touch audit export success: %w", err)
	}
	return nil
}

// TouchAuditExportFailure records a delivery attempt that failed. The
// `lastError` string is truncated to ~512 chars by the service layer so
// a runaway error message doesn't bloat the row.
func (r *Repository) TouchAuditExportFailure(ctx context.Context, tenantID uuid.UUID, lastError string) error {
	const q = `UPDATE audit_export_configs
		SET last_attempt_at = now(),
		    last_error      = $2
		WHERE tenant_id = $1`
	if _, err := r.pool.Exec(ctx, q, tenantID, lastError); err != nil {
		return fmt.Errorf("touch audit export failure: %w", err)
	}
	return nil
}

// IncrementAuditExportDLX bumps the dlx_depth counter when a message
// gets parked after exhausting retries. The drain action (Phase 2
// follow-up) resets the counter via SetAuditExportDLXDepth(0).
func (r *Repository) IncrementAuditExportDLX(ctx context.Context, tenantID uuid.UUID, delta int32) error {
	const q = `UPDATE audit_export_configs
		SET dlx_depth = GREATEST(0, dlx_depth + $2)
		WHERE tenant_id = $1`
	if _, err := r.pool.Exec(ctx, q, tenantID, delta); err != nil {
		return fmt.Errorf("increment audit export dlx: %w", err)
	}
	return nil
}

// scanAuditExportConfig pulls a row out of pgx.Row or pgx.Rows.Scan into
// the typed struct. Centralising the column order here keeps the SELECT
// + RETURNING + future read helpers from drifting.
func scanAuditExportConfig(row pgx.Row) (*AuditExportConfig, error) {
	var c AuditExportConfig
	if err := row.Scan(
		&c.ID, &c.TenantID, &c.Enabled, &c.Format, &c.TargetURL,
		&c.HMACSecret, &c.BearerToken, &c.EventFilters,
		&c.LastSuccessAt, &c.LastAttemptAt, &c.LastError, &c.DLXDepth,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}
