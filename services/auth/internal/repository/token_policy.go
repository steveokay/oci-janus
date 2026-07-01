// Package repository — token_policy.go is the CRUD repository for the
// FUT-003 workspace-wide token policy row.
//
// One row per tenant. All three limit fields are nullable Integer columns:
//
//   - max_ttl_days           — cap on api_keys.expires_at at CreateAPIKey time.
//   - rotation_interval_days — sets api_keys.rotation_due_at on new keys.
//   - idle_revoke_days       — cutoff for the idle-revoke background worker.
//
// NULL means "policy disabled for this dimension" — semantically distinct
// from "zero days", which the service layer rejects at validation time so
// the DB never sees a nonsense value.
//
// All SQL is parameterised (CLAUDE.md §11); no string concatenation.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenPolicy is the in-memory shape of a row in token_policies.
//
// Each of the three limit fields is a pointer so the caller can distinguish
// "not set" (NULL — no cap) from "zero" (which the schema forbids). The
// UpdatedByUserID pointer is nullable for the initial-seed case where no
// operator identity is captured (e.g. bootstrap CLI seeded the row).
type TokenPolicy struct {
	TenantID             uuid.UUID
	MaxTTLDays           *int32
	RotationIntervalDays *int32
	IdleRevokeDays       *int32
	UpdatedAt            time.Time
	UpdatedByUserID      *uuid.UUID
}

// TokenPolicyRepo performs database operations on the token_policies table.
type TokenPolicyRepo struct {
	pool *pgxpool.Pool
}

// NewTokenPolicyRepo constructs a TokenPolicyRepo backed by the given pool.
func NewTokenPolicyRepo(pool *pgxpool.Pool) *TokenPolicyRepo {
	return &TokenPolicyRepo{pool: pool}
}

// GetOrDefault returns the policy for the given tenant. If no row exists,
// returns a zero-valued TokenPolicy (all limit fields nil) with the
// caller's tenantID stamped in — NOT an error. Callers that want to
// distinguish "no policy row" from "policy row with all nil limits" should
// treat both identically per FUT-003 grandfathering semantics.
//
// The zero-value fallback keeps the enforcement path (CreateAPIKey,
// idle_revoke worker) branch-free: they load the policy unconditionally
// and consult individual fields.
func (r *TokenPolicyRepo) GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*TokenPolicy, error) {
	const q = `
		SELECT tenant_id, max_ttl_days, rotation_interval_days, idle_revoke_days,
		       updated_at, updated_by_user_id
		  FROM token_policies WHERE tenant_id = $1`
	var out TokenPolicy
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&out.TenantID, &out.MaxTTLDays, &out.RotationIntervalDays,
		&out.IdleRevokeDays, &out.UpdatedAt, &out.UpdatedByUserID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return &TokenPolicy{TenantID: tenantID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get token policy: %w", err)
	}
	return &out, nil
}

// Upsert inserts or updates the row for the given tenant.
//
// Fields whose pointer is nil are NOT touched on update — a partial update
// that only sets max_ttl_days does not clobber rotation_interval_days or
// idle_revoke_days. The COALESCE(EXCLUDED.field, token_policies.field)
// preserves the old value on nil-in inputs during ON CONFLICT.
//
// updated_by_user_id and updated_at are ALWAYS refreshed on each call
// (even if all limit fields are nil) so the audit trail records who
// touched the row and when. A caller that Puts an all-nil policy is
// semantically saying "no change, but stamp me as the last toucher" —
// unusual but supported.
func (r *TokenPolicyRepo) Upsert(ctx context.Context, in TokenPolicy) (*TokenPolicy, error) {
	const q = `
		INSERT INTO token_policies (tenant_id, max_ttl_days, rotation_interval_days,
		                            idle_revoke_days, updated_by_user_id, updated_at)
		     VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
		    max_ttl_days           = COALESCE(EXCLUDED.max_ttl_days,           token_policies.max_ttl_days),
		    rotation_interval_days = COALESCE(EXCLUDED.rotation_interval_days, token_policies.rotation_interval_days),
		    idle_revoke_days       = COALESCE(EXCLUDED.idle_revoke_days,       token_policies.idle_revoke_days),
		    updated_by_user_id     = EXCLUDED.updated_by_user_id,
		    updated_at             = now()`
	_, err := r.pool.Exec(ctx, q,
		in.TenantID, in.MaxTTLDays, in.RotationIntervalDays, in.IdleRevokeDays, in.UpdatedByUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert token policy: %w", err)
	}
	return r.GetOrDefault(ctx, in.TenantID)
}

// ListTenantsWithIdleRevoke returns every tenant_id that has a non-null
// idle_revoke_days setting. Called by the FUT-003 idle-revoke background
// worker on each tick to enumerate its work list. Tenants without a
// configured idle-revoke policy don't appear — the worker treats absence
// as "no work" rather than "process with default", so a freshly bootstrapped
// deployment does no revoke work until an admin opts in.
func (r *TokenPolicyRepo) ListTenantsWithIdleRevoke(ctx context.Context) ([]uuid.UUID, error) {
	const q = `SELECT tenant_id FROM token_policies WHERE idle_revoke_days IS NOT NULL`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list tenants with idle revoke: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan token policy tenant: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Clear removes the tenant's row entirely (equivalent to "no policy").
// Not exposed at the RPC layer — retained for future support tooling
// (e.g. tenant-delete cascade) and for test setup/teardown.
func (r *TokenPolicyRepo) Clear(ctx context.Context, tenantID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM token_policies WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return fmt.Errorf("clear token policy: %w", err)
	}
	return nil
}
