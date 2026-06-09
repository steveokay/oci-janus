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

// User is the database model for a registry user account.
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Username     string
	Email        string
	PasswordHash string
	IsActive     bool
	FailedLogins int
	LockedUntil  *time.Time
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateUserRequest carries the validated inputs for creating a new user.
type CreateUserRequest struct {
	TenantID     uuid.UUID
	Username     string
	Email        string
	PasswordHash string // pre-hashed with argon2id
}

// UserRepository performs all database operations on the users table.
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository constructs a UserRepository backed by the given pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Create inserts a new user row and returns the persisted record.
// Returns ErrAlreadyExists if (tenant_id, username) or (tenant_id, email) already exist.
func (r *UserRepository) Create(ctx context.Context, req CreateUserRequest) (*User, error) {
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, username, email, password_hash, is_active,
		          failed_logins, locked_until, last_login_at, created_at, updated_at`

	var u User
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.Username, req.Email, req.PasswordHash,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.PasswordHash,
		&u.IsActive, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

// GetByUsername returns the user with the given username in the given tenant.
// Returns ErrNotFound if no such user exists.
func (r *UserRepository) GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, email, password_hash, is_active,
		       failed_logins, locked_until, last_login_at, created_at, updated_at
		FROM   users
		WHERE  tenant_id = $1 AND username = $2`

	return r.scanOne(ctx, q, tenantID, username)
}

// GetByID returns the user with the given primary key.
// Returns ErrNotFound if no such user exists.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, email, password_hash, is_active,
		       failed_logins, locked_until, last_login_at, created_at, updated_at
		FROM   users
		WHERE  id = $1`

	return r.scanOne(ctx, q, id)
}

// RecordFailedLogin increments failed_logins and returns the new count.
// The caller uses the count to decide whether to lock the account.
func (r *UserRepository) RecordFailedLogin(ctx context.Context, id uuid.UUID) (int, error) {
	const q = `
		UPDATE users
		SET    failed_logins = failed_logins + 1, updated_at = now()
		WHERE  id = $1
		RETURNING failed_logins`

	var count int
	if err := r.pool.QueryRow(ctx, q, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("record failed login: %w", err)
	}
	return count, nil
}

// LockUntil sets locked_until so no login is accepted before that timestamp.
func (r *UserRepository) LockUntil(ctx context.Context, id uuid.UUID, until time.Time) error {
	const q = `UPDATE users SET locked_until = $1, updated_at = now() WHERE id = $2`
	_, err := r.pool.Exec(ctx, q, until, id)
	return err
}

// ResetFailedLogins clears failed_logins and locked_until and records the login time.
// Called on every successful authentication.
func (r *UserRepository) ResetFailedLogins(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE users
		SET    failed_logins = 0, locked_until = NULL,
		       last_login_at = now(), updated_at = now()
		WHERE  id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}

// scanOne executes query with args and scans a single User row.
func (r *UserRepository) scanOne(ctx context.Context, query string, args ...any) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.PasswordHash,
		&u.IsActive, &u.FailedLogins, &u.LockedUntil, &u.LastLoginAt,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique constraint violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	// pgx wraps pg errors; check via error message since pgconn.PgError is in a sub-package
	return err != nil && containsCode(err, "23505")
}

func containsCode(err error, code string) bool {
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == code
	}
	return false
}
