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
//
// Email is stored as a nullable column (see migration 20260619000001) but the
// Go struct keeps it as string for backwards-compatibility with existing
// callers; SELECTs use COALESCE(email, '') so NULL becomes "" at the
// application boundary. DisplayName is genuinely optional — callers must
// check for nil rather than treating empty-string as "unset" so they can
// distinguish "user set name to empty" (impossible — handler enforces 1..128)
// from "user has no display name yet".
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Username     string
	Email        string
	DisplayName  *string
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
//
// SELECT/RETURNING wraps email in COALESCE so NULL values (allowed by migration
// 20260619000001) materialise as the empty string in User.Email rather than
// failing the pgx scan into a non-nullable string. display_name is genuinely
// nullable in Go (*string) so it is scanned directly.
func (r *UserRepository) Create(ctx context.Context, req CreateUserRequest) (*User, error) {
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at`

	var u User
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.Username, req.Email, req.PasswordHash,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
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
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at
		FROM   users
		WHERE  tenant_id = $1 AND username = $2`

	return r.scanOne(ctx, q, tenantID, username)
}

// GetByID returns the user with the given primary key.
// Returns ErrNotFound if no such user exists.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at
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

// CountByTenant returns the number of user rows in the tenant (FE-API-028).
// The count intentionally includes inactive users so the platform-admin
// dashboard surfaces the total headcount, not just currently-active sessions.
func (r *UserRepository) CountByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	const q = `SELECT COUNT(*) FROM users WHERE tenant_id = $1`
	var n int64
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count tenant users: %w", err)
	}
	return n, nil
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
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// UpdateProfileRequest carries the optional fields PATCH /users/me may update.
// Each field is a pointer so the repository can distinguish "leave unchanged"
// (nil) from "explicitly clear" (non-nil pointer to empty string for email,
// though the handler currently rejects empty display_name).
type UpdateProfileRequest struct {
	// DisplayName: nil = leave unchanged; non-nil = set to value (may be the
	// empty string only if the caller intends to clear it — handlers should
	// enforce minimum length before calling).
	DisplayName *string
	// Email: nil = leave unchanged; non-nil = set to value. Empty string maps
	// to SQL NULL so callers can clear an email by passing &"".
	Email *string
}

// UpdateProfile mutates the mutable profile fields (display_name, email) and
// returns the refreshed user record. Only fields whose pointer is non-nil are
// touched; other columns are left as-is via COALESCE in the UPDATE statement.
//
// Empty-string email is stored as NULL so the partial-UNIQUE behaviour on
// (tenant_id, email) ignores it (Postgres permits multiple NULLs in a UNIQUE).
// Returns ErrAlreadyExists when the email change collides with another row in
// the same tenant.
func (r *UserRepository) UpdateProfile(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*User, error) {
	// Convert *string into the pair (set?, value) so the SQL CASE can decide
	// whether to substitute or keep the existing column. Using a sentinel
	// boolean is clearer than NULLIF tricks and keeps the query parameterised.
	var (
		setName  bool
		nameVal  string
		setEmail bool
		emailVal *string // nil => store NULL; non-nil => store the string
	)
	if req.DisplayName != nil {
		setName = true
		nameVal = *req.DisplayName
	}
	if req.Email != nil {
		setEmail = true
		if *req.Email != "" {
			v := *req.Email
			emailVal = &v
		}
	}

	const q = `
		UPDATE users
		SET    display_name = CASE WHEN $2::bool THEN NULLIF($3::text, '') ELSE display_name END,
		       email        = CASE WHEN $4::bool THEN $5::text             ELSE email        END,
		       updated_at   = now()
		WHERE  id = $1
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at`

	var u User
	err := r.pool.QueryRow(ctx, q, id, setName, nameVal, setEmail, emailVal).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return &u, nil
}

// UpdatePasswordHash overwrites the stored argon2id hash for the given user.
// Callers must have already verified the current password and validated the
// new one against the password policy before invoking this method.
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, id uuid.UUID, newHash string) error {
	const q = `UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`
	tag, err := r.pool.Exec(ctx, q, newHash, id)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
