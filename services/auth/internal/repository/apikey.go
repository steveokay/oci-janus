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
type APIKey struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Name        string
	KeyHash     string // argon2id hash of the raw secret
	KeyPrefix   string // first 12 chars of raw key, for display only
	Scopes      []string
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	IsActive    bool
	CreatedAt   time.Time
}

// CreateAPIKeyRequest carries pre-validated data for inserting a new key.
type CreateAPIKeyRequest struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Name      string
	KeyHash   string
	KeyPrefix string
	Scopes    []string
	ExpiresAt *time.Time
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
// Returns ErrAlreadyExists if the user already has a key with the same name.
func (r *APIKeyRepository) Create(ctx context.Context, req CreateAPIKeyRequest) (*APIKey, error) {
	const q = `
		INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, user_id, name, key_hash, key_prefix,
		          scopes, expires_at, last_used_at, is_active, created_at`

	var k APIKey
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.UserID, req.Name, req.KeyHash,
		req.KeyPrefix, req.Scopes, req.ExpiresAt,
	).Scan(
		&k.ID, &k.TenantID, &k.UserID, &k.Name, &k.KeyHash, &k.KeyPrefix,
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
		SELECT id, tenant_id, user_id, name, key_hash, key_prefix,
		       scopes, expires_at, last_used_at, is_active, created_at
		FROM   api_keys
		WHERE  id = $1 AND is_active = true`

	var k APIKey
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&k.ID, &k.TenantID, &k.UserID, &k.Name, &k.KeyHash, &k.KeyPrefix,
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

// ListByUser returns all active API keys owned by the given user.
func (r *APIKeyRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*APIKey, error) {
	const q = `
		SELECT id, tenant_id, user_id, name, key_hash, key_prefix,
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
			&k.ID, &k.TenantID, &k.UserID, &k.Name, &k.KeyHash, &k.KeyPrefix,
			&k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.IsActive, &k.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// Delete soft-deletes the key by setting is_active=false.
// Only the owning user can delete their own key (enforced via userID check).
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

// TouchLastUsed records the current time as last_used_at. Called after successful validation.
func (r *APIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE api_keys SET last_used_at = now() WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}
