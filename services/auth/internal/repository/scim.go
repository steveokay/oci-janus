package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// scimUserColumns is the canonical SELECT column list shared by the SCIM user
// reads. It matches the standard User scan order (id, tenant_id, username,
// email, display_name, password_hash, is_active, failed_logins, locked_until,
// last_login_at, created_at, updated_at, kind, is_global_admin,
// onboarding_complete, sso_subject) and appends external_id as a trailing
// column so the SCIM reads can echo the IdP correlation key. Scan every row
// selecting these columns with scanSCIMUserRow.
const scimUserColumns = `id, tenant_id, username, COALESCE(email, ''), display_name,
	       password_hash, is_active, failed_logins, locked_until,
	       last_login_at, created_at, updated_at, kind, is_global_admin, onboarding_complete,
	       COALESCE(sso_subject, ''), COALESCE(external_id, '')`

// scanSCIMUserRow scans a single row selecting scimUserColumns (the standard
// User columns + trailing external_id) into a User. row is a pgx.Row or
// pgx.Rows.
func scanSCIMUserRow(row pgx.Row, u *User) error {
	return row.Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind, &u.IsGlobalAdmin, &u.OnboardingComplete,
		&u.SSOSubject, &u.ExternalID,
	)
}

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

// CreateSCIMUser inserts an IdP-provisioned user: passwordless (empty
// password_hash), kind='human', stamped with external_id + provisioned_via='scim'.
// Mirrors CreateSSOUser's passwordless INSERT (user.go) with the SCIM columns
// swapped in. Returns ErrAlreadyExists on a (tenant_id, username|email|
// external_id) collision so the service can fall back to link-by-email (spec
// D3). The RETURNING column list matches scanOne's scan order exactly.
func (r *UserRepository) CreateSCIMUser(ctx context.Context, tenantID uuid.UUID, username, email, displayName, externalID string) (*User, error) {
	q := `
		INSERT INTO users (tenant_id, username, email, password_hash, display_name,
		                   external_id, provisioned_via, kind)
		VALUES ($1, $2, NULLIF($3, ''), '', NULLIF($4, ''), $5, 'scim', 'human')
		RETURNING ` + scimUserColumns

	var u User
	err := scanSCIMUserRow(r.pool.QueryRow(ctx, q, tenantID, username, email, displayName, externalID), &u)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create scim user: %w", err)
	}
	return &u, nil
}

// GetUserByExternalID returns the user with this external_id in the tenant, or
// ErrNotFound. Used for SCIM read + re-provision idempotency. Reuses the
// standard user SELECT column list (scimUserColumns) with a (tenant_id,
// external_id) predicate.
func (r *UserRepository) GetUserByExternalID(ctx context.Context, tenantID uuid.UUID, externalID string) (*User, error) {
	q := `SELECT ` + scimUserColumns + `
		FROM   users
		WHERE  tenant_id = $1 AND external_id = $2`
	var u User
	err := scanSCIMUserRow(r.pool.QueryRow(ctx, q, tenantID, externalID), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetSCIMUserByIDForTenant loads a single user by primary key, scoped to the
// SCIM tenant, selecting the full SCIM column set (including external_id) so the
// by-id / PUT / PATCH / DELETE responses echo the IdP correlation key. The
// standard GetByID→scanOne path omits external_id, which left those responses
// carrying externalId:"" and broke Okta/Entra reconciliation — this read exists
// so the SCIM surface always round-trips external_id. Returns ErrNotFound when
// no row with this (id, tenant_id) exists, so it can never read across tenants.
func (r *UserRepository) GetSCIMUserByIDForTenant(ctx context.Context, tenantID, userID uuid.UUID) (*User, error) {
	q := `SELECT ` + scimUserColumns + `
		FROM   users
		WHERE  id = $1 AND tenant_id = $2`
	var u User
	err := scanSCIMUserRow(r.pool.QueryRow(ctx, q, userID, tenantID), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// SetExternalID backfills external_id + provisioned_via on an existing user
// (the link-passwordless path, spec D3). Scoped by tenant_id + id.
func (r *UserRepository) SetExternalID(ctx context.Context, tenantID, userID uuid.UUID, externalID string) error {
	const q = `UPDATE users SET external_id = $3, provisioned_via = 'scim', updated_at = now()
	           WHERE id = $2 AND tenant_id = $1`
	_, err := r.pool.Exec(ctx, q, tenantID, userID, externalID)
	return err
}

// ListSCIMUsers returns up to `count` users starting at 1-based `startIndex`,
// filtered by the optional exact-match predicates, plus the total count for the
// filtered set. Empty filter strings match all; activeFilter == nil matches
// all. Ordered by created_at (then id, for stability) so pages are stable.
//
// The WHERE clause is assembled from a parameterised predicate slice — user
// values are NEVER fmt.Sprintf'd into SQL (CLAUDE.md §11). Only the column
// names in the predicates are literal, and those come from the parser, not the
// caller.
func (r *UserRepository) ListSCIMUsers(ctx context.Context, tenantID uuid.UUID, byUsername, byExternalID string, activeFilter *bool, startIndex, count int) (users []*User, total int, err error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if byUsername != "" {
		where = append(where, "username = "+next(byUsername))
	}
	if byExternalID != "" {
		where = append(where, "external_id = "+next(byExternalID))
	}
	if activeFilter != nil {
		where = append(where, "is_active = "+next(*activeFilter))
	}
	whereClause := ""
	for i, w := range where {
		if i == 0 {
			whereClause = " WHERE " + w
			continue
		}
		whereClause += " AND " + w
	}

	// Total count of the filtered set (before pagination).
	countQ := `SELECT COUNT(*) FROM users` + whereClause
	if err = r.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scim users: %w", err)
	}

	// Page: LIMIT/OFFSET (OFFSET = startIndex-1, 1-based per SCIM RFC 7644).
	limitPlaceholder := next(count)
	offsetPlaceholder := next(startIndex - 1)
	pageQ := `SELECT ` + scimUserColumns + ` FROM users` + whereClause +
		` ORDER BY created_at, id LIMIT ` + limitPlaceholder + ` OFFSET ` + offsetPlaceholder

	rows, err := r.pool.Query(ctx, pageQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list scim users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u User
		if err = scanSCIMUserRow(rows, &u); err != nil {
			return nil, 0, fmt.Errorf("scan scim user: %w", err)
		}
		uu := u
		users = append(users, &uu)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate scim users: %w", err)
	}
	return users, total, nil
}
