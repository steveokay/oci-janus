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
//
// Kind holds the value of the users.kind column (migration 20260622000001):
// "human" for interactive accounts, "service_account" for machine identities
// that back a service_accounts row. All single-row lookups on the human login
// path use GetHuman* variants so the kind guard is enforced at the repository
// layer rather than scattered across callers.
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
	// Kind is "human" or "service_account" (added by migration 20260622000001).
	Kind string
}

// CreateUserRequest carries the validated inputs for creating a new user.
type CreateUserRequest struct {
	TenantID     uuid.UUID
	Username     string
	Email        string
	PasswordHash string // pre-hashed with argon2id
	// DisplayName is the human-friendly name for the account. Empty string
	// inserts SQL NULL via NULLIF so the column stays comparable to the
	// SSO-provisioned and shadow-user paths. The HTTP handler enforces
	// non-empty for the public POST /api/v1/users route (REM-018), but
	// internal callers (e.g. shadow-user creation for service accounts)
	// may still pass empty.
	DisplayName string
	// Kind sets the users.kind column; defaults to "human" when empty.
	// Pass "service_account" only when creating a shadow user for a
	// service_accounts row — all other callers should leave this unset.
	Kind string
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
//
// req.Kind defaults to "human" when the zero-value is passed; callers that
// create shadow users for service accounts must pass "service_account"
// explicitly.
func (r *UserRepository) Create(ctx context.Context, req CreateUserRequest) (*User, error) {
	// Default the kind to "human" so existing callers that do not set the field
	// continue to work after migration 20260622000001 added the column.
	kind := req.Kind
	if kind == "" {
		kind = "human"
	}

	// REM-018: NULLIF($X, '') keeps the column NULL when DisplayName is the
	// zero value (internal callers like service-account shadow rows) and
	// stores the literal string otherwise.
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash, display_name, kind)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at, kind`

	var u User
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.Username, req.Email, req.PasswordHash, req.DisplayName, kind,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

// GetByUsername returns the user with the given username in the given tenant,
// regardless of kind. Prefer GetHumanByUsername on the login path so that a
// service-account shadow user cannot authenticate as a human.
// Returns ErrNotFound if no such user exists.
func (r *UserRepository) GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at, kind
		FROM   users -- allow-any-kind: username lookup is kind-agnostic; callers on the
		             -- human login path must use GetHumanByUsername instead (FE-API-048 §4.1)
		WHERE  tenant_id = $1 AND username = $2`

	return r.scanOne(ctx, q, tenantID, username)
}

// GetByID returns the user with the given primary key, regardless of kind.
// Callers on the human authentication path should use GetHumanByID so that a
// service-account shadow user cannot be loaded as a human identity. This
// variant is kept for internal usage (e.g. RBAC lookups that apply to both
// humans and service accounts).
// Returns ErrNotFound if no such user exists.
//
// Deprecated: use GetHumanByID on the human-auth path or GetUserAnyKind when
// the caller explicitly needs to see shadow users. Wrapper will be removed
// once all callers are migrated (FE-API-048 cleanup, Task 10+).
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	return r.GetUserAnyKind(ctx, id)
}

// GetByEmail returns the user with the given email in the given tenant,
// regardless of kind. Prefer GetHumanByEmail on the SSO/login path so that
// service-account synthetic emails (sa+N@internal.invalid) cannot match.
// Used by the FE-API-034 SSO callback to match an IdP-provided email to an
// existing account before deciding whether to auto-provision. Email match is
// case-insensitive at the application layer — IdPs are inconsistent about
// casing so we compare lowercase.
//
// Returns ErrNotFound if no row matches.
//
// Deprecated: use GetHumanByEmail on the human authentication path.
func (r *UserRepository) GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*User, error) {
	return r.GetHumanByEmail(ctx, tenantID, email)
}

// CreateSSOUserRequest carries the validated inputs for provisioning an
// account from an SSO callback. PasswordHash is intentionally empty — SSO
// users authenticate via the IdP, never via the local password endpoint.
//
// REDESIGN-001 RM-003: SSOProviderID changed from uuid.UUID to string to match
// the global_sso_config.provider_id stable string identifier.
type CreateSSOUserRequest struct {
	TenantID      uuid.UUID
	Username      string
	Email         string
	DisplayName   string
	SSOProviderID string // stable string id from global_sso_config (e.g. "google")
}

// CreateSSOUser inserts a user provisioned from an SSO callback.
// password_hash is set to the empty string so the local password login path
// cannot succeed for this account (ValidatePassword rejects "" and Argon2
// verify never returns true against an empty hash).
//
// SSO users always have kind='human'; they are interactive accounts driven
// by a human at an IdP, never machine identities.
//
// Returns ErrAlreadyExists on a (tenant_id, username) or (tenant_id, email)
// collision — the caller may then re-query by email and treat that row as
// the same user (race with another concurrent SSO callback).
func (r *UserRepository) CreateSSOUser(ctx context.Context, req CreateSSOUserRequest) (*User, error) {
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash,
		                   display_name, sso_provider_id, kind)
		VALUES ($1, $2, NULLIF($3, ''), '', NULLIF($4, ''), $5, 'human')
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at, kind`

	var u User
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.Username, req.Email, req.DisplayName, req.SSOProviderID,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create sso user: %w", err)
	}
	return &u, nil
}

// TouchLastLogin sets last_login_at to NOW for an existing user. Called from
// the SSO callback for users that already exist, so the audit timeline shows
// the most recent SSO authentication.
func (r *UserRepository) TouchLastLogin(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE users SET last_login_at = NOW(), updated_at = NOW() WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
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
//
// Deprecated: use CountHumans so that service-account shadow rows are
// excluded from the headcount. This wrapper is kept until all callers are
// migrated (end of Task 6, FE-API-048).
func (r *UserRepository) CountByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return r.CountHumans(ctx, tenantID)
}

// ── kind-guarded helpers (FE-API-048, spec §4.1) ──────────────────────────────
//
// These methods enforce kind='human' at the SQL layer so every caller on the
// human authentication and management paths inherits the guard without
// additional application-level checks. Service-account shadow users are
// silently excluded from all …Human… results.

// ListHumans returns all users with kind='human' in the given tenant, ordered
// by created_at descending. Service-account shadow rows (kind='service_account')
// are excluded at the SQL layer. The count includes inactive users so the
// management UI can surface deactivated accounts.
//
// opts is reserved for future pagination support and is currently unused.
func (r *UserRepository) ListHumans(ctx context.Context, tenantID uuid.UUID, opts ListOpts) ([]User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at, kind
		FROM   users -- kind = 'human' enforced in WHERE below (FE-API-048 §4.1)
		WHERE  tenant_id = $1 AND kind = 'human'
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list humans: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
			&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
			&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind,
		); err != nil {
			return nil, fmt.Errorf("scan human user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListOpts carries optional parameters for list queries. Currently a
// placeholder; pagination fields (page_size, page_token) will be added here
// in a later task so callers do not need to change their signatures.
type ListOpts struct{}

// GetHumanByEmail returns the human user with the given email in the given
// tenant. Service-account synthetic emails (sa+N@internal.invalid) are
// excluded by the kind='human' guard so they can never match.
//
// Email comparison is case-insensitive — IdPs are inconsistent about casing.
// Returns ErrNotFound if no human row matches.
func (r *UserRepository) GetHumanByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at, kind
		FROM   users -- kind = 'human' enforced in WHERE below (FE-API-048 §4.1)
		WHERE  tenant_id = $1 AND LOWER(email) = LOWER($2) AND kind = 'human'`

	return r.scanOne(ctx, q, tenantID, email)
}

// GetHumanByID returns the user with the given primary key only when its
// kind='human'. If the ID belongs to a service-account shadow user
// (kind='service_account') the method returns ErrNotFound, preventing SA
// credentials from being loaded onto a human identity context.
func (r *UserRepository) GetHumanByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at, kind
		FROM   users -- kind = 'human' enforced in WHERE below (FE-API-048 §4.1)
		WHERE  id = $1 AND kind = 'human'`

	return r.scanOne(ctx, q, id)
}

// CountHumans returns the number of users with kind='human' in the given
// tenant. Service-account shadow rows are excluded so the count reflects
// the real human headcount for billing / plan limits.
// Inactive human users are included (same rationale as CountByTenant).
func (r *UserRepository) CountHumans(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	const q = `SELECT COUNT(*) FROM users WHERE tenant_id = $1 AND kind = 'human'`
	var n int64
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count humans: %w", err)
	}
	return n, nil
}

// GetUserAnyKind returns the user with the given primary key regardless of
// kind. Use this only when the caller genuinely needs to load a
// service-account shadow user (e.g. the SA management handlers). All
// human-facing authentication paths must use GetHumanByID instead.
// Returns ErrNotFound if no row with that ID exists.
func (r *UserRepository) GetUserAnyKind(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `
		SELECT id, tenant_id, username, COALESCE(email, ''), display_name,
		       password_hash, is_active, failed_logins, locked_until,
		       last_login_at, created_at, updated_at, kind
		FROM   users -- allow-any-kind: intentional — SA management handlers need shadow users
		WHERE  id = $1`

	return r.scanOne(ctx, q, id)
}

// UserSummary is the lightweight (id, username, display_name) shape returned
// by LookupByIDs. It's the minimum a caller needs to render a user-facing
// label and intentionally omits everything else so the wire shape stays
// narrow even when the lookup batch is large.
type UserSummary struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// LookupByIDs batch-resolves users by their primary key within a tenant.
// REM-018-followup: services/management calls this from
// `/api/v1/notifications` enrichment so the activity feed renders
// display_name instead of a raw UUID.
//
// DisplayName uses the same COALESCE fallback chain as ListMembers
// (service_accounts.name → users.display_name → users.username →
// users.email) so the caller doesn't have to know about service accounts
// or the email backstop. The shadow_user_id LEFT JOIN on service_accounts
// keeps SA-driven actions (e.g. `push.image` by a bot) rendering as the
// SA name instead of the auto-generated shadow username.
//
// Tenant isolation is enforced in the WHERE clause; ids outside the
// tenant are dropped silently — the caller treats absence as "not in this
// tenant, render UUID fallback".
func (r *UserRepository) LookupByIDs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]UserSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const q = `
		SELECT u.id,
		       u.username,
		       COALESCE(sa.name, u.display_name, u.username, COALESCE(u.email, '')) AS display_name
		FROM   users u
		LEFT   JOIN service_accounts sa ON sa.shadow_user_id = u.id
		WHERE  u.tenant_id = $1
		  AND  u.id        = ANY($2)`
	rows, err := r.pool.Query(ctx, q, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("lookup users by ids: %w", err)
	}
	defer rows.Close()
	out := make([]UserSummary, 0, len(ids))
	for rows.Next() {
		var s UserSummary
		if err := rows.Scan(&s.ID, &s.Username, &s.DisplayName); err != nil {
			return nil, fmt.Errorf("scan user summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
// Every SELECT that feeds into this helper must include 'kind' as the last
// column in the select list (migration 20260622000001 added the column).
func (r *UserRepository) scanOne(ctx context.Context, query string, args ...any) (*User, error) {
	var u User
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind,
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
		          last_login_at, created_at, updated_at, kind`

	var u User
	err := r.pool.QueryRow(ctx, q, id, setName, nameVal, setEmail, emailVal).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind,
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
