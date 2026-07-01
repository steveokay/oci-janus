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

// APIKey is the database model for a robot / machine-account API key.
//
// Ownership is polymorphic: exactly one of UserID and ServiceAccountID is
// non-nil. A human-owned key has UserID set and ServiceAccountID nil; a
// service-account-owned key has ServiceAccountID set and UserID nil. This is
// enforced at the database layer via the CHECK constraint
// api_keys_owner_exactly_one (migration 20260622000003). Callers must branch on
// whichever field is non-nil to determine the key owner type.
type APIKey struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// UserID is non-nil for human-owned keys, nil for SA-owned keys.
	UserID *uuid.UUID
	// ServiceAccountID is non-nil for SA-owned keys, nil for human-owned keys.
	ServiceAccountID *uuid.UUID
	Name             string
	KeyHash          string // argon2id hash of the raw secret
	KeyPrefix        string // first 12 chars of raw key, for display only
	Scopes           []string
	ExpiresAt        *time.Time
	LastUsedAt       *time.Time
	IsActive         bool
	CreatedAt        time.Time
}

// CreateAPIKeyRequest carries pre-validated data for inserting a new key.
// Exactly one of UserID and ServiceAccountID must be non-nil. Both nil or both
// set is a programming error; the repository enforces this before reaching the
// database CHECK constraint so callers get a descriptive error rather than a
// raw PG violation.
type CreateAPIKeyRequest struct {
	TenantID uuid.UUID
	// UserID must be non-nil for human-owned keys; nil for SA-owned keys.
	UserID *uuid.UUID
	// ServiceAccountID must be non-nil for SA-owned keys; nil for human-owned keys.
	ServiceAccountID *uuid.UUID
	Name             string
	KeyHash          string
	KeyPrefix        string
	Scopes           []string
	ExpiresAt        *time.Time
}

// APIKeyRepository performs database operations on the api_keys table.
type APIKeyRepository struct {
	pool *pgxpool.Pool
}

// NewAPIKeyRepository constructs an APIKeyRepository backed by the given pool.
func NewAPIKeyRepository(pool *pgxpool.Pool) *APIKeyRepository {
	return &APIKeyRepository{pool: pool}
}

// Create inserts a new API key row and returns the persisted record.
//
// Exactly one of req.UserID and req.ServiceAccountID must be non-nil. Returns
// an error immediately (before hitting the database) when this invariant is
// violated, so callers get a clear message rather than a raw Postgres CHECK
// violation. The database CHECK constraint remains the authoritative backstop.
//
// Returns ErrAlreadyExists when the same owner already has a key with the same
// name (partial unique index violation).
func (r *APIKeyRepository) Create(ctx context.Context, req CreateAPIKeyRequest) (*APIKey, error) {
	// Defence-in-depth ownership check — the database CHECK is the backstop but
	// we surface a clear error at the application layer so callers get useful
	// feedback without parsing Postgres error detail strings.
	bothNil := req.UserID == nil && req.ServiceAccountID == nil
	bothSet := req.UserID != nil && req.ServiceAccountID != nil
	if bothNil || bothSet {
		return nil, fmt.Errorf("apikey: exactly one of UserID/ServiceAccountID must be set")
	}

	// Normalise nil scopes to an empty slice so pgx serialises as '{}'
	// rather than NULL, which would violate the NOT NULL DEFAULT '{}' column.
	if req.Scopes == nil {
		req.Scopes = []string{}
	}

	const q = `
		INSERT INTO api_keys (tenant_id, user_id, service_account_id, name, key_hash, key_prefix, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, tenant_id, user_id, service_account_id, name, key_hash, key_prefix,
		          scopes, expires_at, last_used_at, is_active, created_at`

	var k APIKey
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.UserID, req.ServiceAccountID, req.Name, req.KeyHash,
		req.KeyPrefix, req.Scopes, req.ExpiresAt,
	).Scan(
		&k.ID, &k.TenantID, &k.UserID, &k.ServiceAccountID, &k.Name, &k.KeyHash, &k.KeyPrefix,
		&k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.IsActive, &k.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return &k, nil
}

// GetByID returns the API key with the given primary key.
// Returns ErrNotFound if the key does not exist or is inactive.
func (r *APIKeyRepository) GetByID(ctx context.Context, id uuid.UUID) (*APIKey, error) {
	const q = `
		SELECT id, tenant_id, user_id, service_account_id, name, key_hash, key_prefix,
		       scopes, expires_at, last_used_at, is_active, created_at
		FROM   api_keys
		WHERE  id = $1 AND is_active = true`

	var k APIKey
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&k.ID, &k.TenantID, &k.UserID, &k.ServiceAccountID, &k.Name, &k.KeyHash, &k.KeyPrefix,
		&k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.IsActive, &k.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return &k, nil
}

// ListByUser returns all active API keys owned by the given human user.
// Rows with service_account_id set are excluded by the WHERE clause so this
// method only returns human-owned keys.
//
// Structurally similar to ListByServiceAccount below but scopes to user_id
// vs service_account_id — different RBAC contracts make the duplication
// intentional; collapsing them via a shared helper that takes a column name
// would let a caller accidentally swap scopes.
//
//nolint:dupl // intentional — see comment above
func (r *APIKeyRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*APIKey, error) {
	const q = `
		SELECT id, tenant_id, user_id, service_account_id, name, key_hash, key_prefix,
		       scopes, expires_at, last_used_at, is_active, created_at
		FROM   api_keys
		WHERE  user_id = $1 AND is_active = true
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(
			&k.ID, &k.TenantID, &k.UserID, &k.ServiceAccountID, &k.Name, &k.KeyHash, &k.KeyPrefix,
			&k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.IsActive, &k.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// ListByServiceAccount returns all active API keys owned by the given service
// account. Rows with user_id set are excluded by the WHERE clause so this
// method only returns SA-owned keys.
//
//nolint:dupl // See sibling ListByUser above — duplication is intentional.
func (r *APIKeyRepository) ListByServiceAccount(ctx context.Context, saID uuid.UUID) ([]*APIKey, error) {
	const q = `
		SELECT id, tenant_id, user_id, service_account_id, name, key_hash, key_prefix,
		       scopes, expires_at, last_used_at, is_active, created_at
		FROM   api_keys
		WHERE  service_account_id = $1 AND is_active = true
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, saID)
	if err != nil {
		return nil, fmt.Errorf("list sa api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(
			&k.ID, &k.TenantID, &k.UserID, &k.ServiceAccountID, &k.Name, &k.KeyHash, &k.KeyPrefix,
			&k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.IsActive, &k.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan sa api key: %w", err)
		}
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// Delete soft-deletes a human-owned API key by setting is_active=false.
// SA-owned keys must be removed via DeleteByServiceAccount because the
// WHERE user_id = $2 predicate never matches NULL in Postgres — using this
// method for an SA-owned key always returns ErrNotFound even when the key
// exists. The two paths are deliberately separate so the wrong owner-column
// cannot authorise a delete.
func (r *APIKeyRepository) Delete(ctx context.Context, id, userID uuid.UUID) error {
	const q = `UPDATE api_keys SET is_active = false WHERE id = $1 AND user_id = $2`
	tag, err := r.pool.Exec(ctx, q, id, userID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteByServiceAccount soft-deletes an SA-owned API key by setting
// is_active=false. Returns ErrNotFound when no (id, service_account_id)
// pair exists. Use Delete (the method above) for human-owned keys — the two
// paths are deliberately separate so the wrong owner-column cannot authorise
// a delete.
func (r *APIKeyRepository) DeleteByServiceAccount(ctx context.Context, id, saID uuid.UUID) error {
	const q = `UPDATE api_keys SET is_active = false WHERE id = $1 AND service_account_id = $2`
	tag, err := r.pool.Exec(ctx, q, id, saID)
	if err != nil {
		return fmt.Errorf("delete sa api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchLastUsed records the current time as last_used_at. Called after successful validation.
func (r *APIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE api_keys SET last_used_at = now() WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}

// UpdateLastUsedAt bumps the timestamp on the given key to the caller-supplied
// time. Preferred over TouchLastUsed on the FUT-003 debounced updater path so
// tests can pin the wall clock. Misses (e.g. Redis unreachable + debounce
// skipped concurrently) are tolerated because the worst-case impact is a
// slightly-later idle-revoke evaluation — the security boundary is the DB
// row-state check on the auth hot path, not the last_used_at value.
func (r *APIKeyRepository) UpdateLastUsedAt(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, at)
	return err
}

// SetRotationDueAt records the deadline for a required rotation. Called
// during CreateAPIKey when the workspace policy has rotation_interval_days.
// A nil `at` clears the deadline (e.g. after an operator rotates the key
// manually and wants to opt-out of further reminders).
func (r *APIKeyRepository) SetRotationDueAt(ctx context.Context, id uuid.UUID, at *time.Time) error {
	const q = `UPDATE api_keys SET rotation_due_at = $2 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, at)
	return err
}

// RevokeWithReason soft-deletes an API key by flipping is_active=false and
// recording a reason string ("manual" | "idle_revoked" | "rotation_lapsed").
// Unlike Delete/DeleteByServiceAccount, this method is NOT scoped to an
// owner — it's used by the FUT-003 idle-revoke worker which iterates by
// tenant and doesn't know the owner shape in advance.
//
// Returns ErrNotFound when no row matches. The two-column UPDATE (is_active
// + revoke_reason) runs in a single statement so a concurrent read never
// sees the "revoked without a reason" transient.
func (r *APIKeyRepository) RevokeWithReason(ctx context.Context, id uuid.UUID, reason string) error {
	const q = `UPDATE api_keys SET is_active = false, revoke_reason = $2 WHERE id = $1 AND is_active = true`
	tag, err := r.pool.Exec(ctx, q, id, reason)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IdleKey is the projection ListIdleKeys returns — the columns the idle-
// revoke worker + audit emitter both need. Kept narrow so a large tenant
// with tens of thousands of live keys doesn't pull unnecessary bytes over
// the wire.
type IdleKey struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	UserID           *uuid.UUID
	ServiceAccountID *uuid.UUID
	LastUsedAt       *time.Time
}

// ListIdleKeys returns active (non-revoked) keys whose last_used_at is
// older than the given cutoff, restricted to the given tenant. Rows with
// NULL last_used_at are ALSO returned — a key that was created but never
// used is idle by definition, and letting it linger indefinitely would
// defeat the purpose of the idle-revoke policy.
//
// Uses the partial idx_api_keys_idle_check index (WHERE is_active = true)
// so scans are proportional to the tenant's live-key count. The worker
// calls this once per tick per configured tenant.
func (r *APIKeyRepository) ListIdleKeys(ctx context.Context, tenantID uuid.UUID, cutoff time.Time) ([]IdleKey, error) {
	const q = `
		SELECT id, tenant_id, user_id, service_account_id, last_used_at
		  FROM api_keys
		 WHERE tenant_id = $1
		   AND is_active = true
		   AND (last_used_at IS NULL OR last_used_at < $2)`
	rows, err := r.pool.Query(ctx, q, tenantID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list idle keys: %w", err)
	}
	defer rows.Close()
	var out []IdleKey
	for rows.Next() {
		var k IdleKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.UserID, &k.ServiceAccountID, &k.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan idle key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
