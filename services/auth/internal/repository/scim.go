package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SCIMConfig is the single global SCIM provisioning config row.
type SCIMConfig struct {
	TenantID   uuid.UUID
	TokenHash  string // Argon2id PHC; "" when never set / disabled
	Enabled    bool
	LastUsedAt *time.Time
}

// GetSCIMConfig returns the singleton scim_config row, or ErrNotFound when it
// has never been written (feature never configured).
func (r *UserRepository) GetSCIMConfig(ctx context.Context) (*SCIMConfig, error) {
	const q = `SELECT tenant_id, COALESCE(token_hash, ''), enabled, last_used_at
	           FROM scim_config WHERE id = 1`
	var c SCIMConfig
	err := r.pool.QueryRow(ctx, q).Scan(&c.TenantID, &c.TokenHash, &c.Enabled, &c.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertSCIMToken writes the singleton row with a new Argon2 token hash and
// enables the feature. tokenHash == "" with enabled=false disables it.
func (r *UserRepository) UpsertSCIMToken(ctx context.Context, tenantID uuid.UUID, tokenHash string, enabled bool) error {
	const q = `
		INSERT INTO scim_config (id, tenant_id, token_hash, enabled, rotated_at)
		VALUES (1, $1, NULLIF($2, ''), $3, now())
		ON CONFLICT (id) DO UPDATE
		  SET tenant_id = EXCLUDED.tenant_id,
		      token_hash = EXCLUDED.token_hash,
		      enabled = EXCLUDED.enabled,
		      rotated_at = now()`
	_, err := r.pool.Exec(ctx, q, tenantID, tokenHash, enabled)
	return err
}

// TouchSCIMLastUsed best-effort stamps last_used_at. Errors are ignored by the
// caller (it is an audit convenience, not a security gate).
func (r *UserRepository) TouchSCIMLastUsed(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `UPDATE scim_config SET last_used_at = now() WHERE id = 1`)
	return err
}
