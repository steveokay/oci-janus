package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FUT-012 Phase A — tenant-user lifecycle management repository methods.
//
// Three operations back the new RPCs:
//
//   ListTenantUsers  — paginated tenant member list with the role
//                      aggregate (org admin/writer/reader counts +
//                      tenant_admin / platform_admin flags).
//   CreateInvitedUser — inserts a users row in 'invited' status with
//                       the stored invite_token_hash + expiry. The
//                       caller hashes the raw token (argon2id) before
//                       handing it here; the raw value never lands in
//                       the DB.
//   SetUserStatus    — flips users.status between 'active' and
//                      'disabled', returning the new value so the
//                      caller can echo it on the wire.
//
// All three SCAN tenant_id explicitly even though most are also
// expressed in the WHERE clause — defence in depth so a future code
// change can't accidentally cross tenants.

// TenantUserSummary is the per-row shape ListTenantUsers returns.
// Mirrors the proto TenantUser message field-for-field so the gRPC
// handler can pass through without re-shaping.
//
// DisplayName is "" when the user hasn't set one — the FE UserCell
// falls back to @username in that case. LastLoginAt is a pointer so
// "never logged in" is distinguishable from "logged in at the zero
// time" (the proto handler emits nil when nil and a Timestamp
// otherwise).
type TenantUserSummary struct {
	UserID      uuid.UUID
	Username    string
	DisplayName string
	Email       string
	Kind        string
	Status      string
	LastLoginAt *time.Time
	CreatedAt   time.Time
	// Role aggregate — populated by the same query via a LATERAL +
	// FILTER aggregate so a single round-trip returns everything the
	// handler needs to build a RoleSummary.
	OrgAdminCount  int32
	OrgWriterCount int32
	OrgReaderCount int32
	RepoGrantCount int32
	TenantAdmin    bool
	PlatformAdmin  bool
}

// ListTenantUsersOpts carries the cursor + page size. PageSize <= 0
// falls back to the default; > maxPageSize is clamped. The cursor
// encodes the last seen (created_at, user_id) pair so pagination is
// stable across concurrent writes.
type ListTenantUsersOpts struct {
	PageSize  int32
	PageToken string
}

const (
	defaultTenantUsersPageSize int32 = 50
	maxTenantUsersPageSize     int32 = 200
)

// ListTenantUsers returns one page of users within the tenant, plus a
// next_page_token (empty when the page is the last one) and the total
// row count so the FE can render a "N users" header without a separate
// COUNT call.
//
// The role-aggregate sub-selects use FILTER + COUNT against a single
// JOIN'd role_assignments scan so the planner can satisfy the whole
// query with one index range on (tenant_id, user_id). Don't unroll
// this into N round-trips per row — that's the regression this design
// avoids.
func (r *UserRepository) ListTenantUsers(
	ctx context.Context,
	tenantID uuid.UUID,
	opts ListTenantUsersOpts,
) ([]TenantUserSummary, string, int32, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultTenantUsersPageSize
	}
	if pageSize > maxTenantUsersPageSize {
		pageSize = maxTenantUsersPageSize
	}

	// page_token is an opaque RFC3339Nano timestamp + UUID cursor.
	// Empty page_token means "first page" — no cursor predicate.
	var (
		cursorTime time.Time
		cursorID   uuid.UUID
		hasCursor  bool
	)
	if opts.PageToken != "" {
		ct, cid, err := decodeUserPageToken(opts.PageToken)
		if err != nil {
			return nil, "", 0, fmt.Errorf("invalid page_token: %w", err)
		}
		cursorTime = ct
		cursorID = cid
		hasCursor = true
	}

	// Total count is computed in a separate single-row SELECT instead
	// of a window function so the planner doesn't have to materialise
	// the full result set just to count it. Tenant total user counts
	// are typically <10k so the extra round-trip is cheap.
	const countQ = `SELECT COUNT(*) FROM users WHERE tenant_id = $1`
	var total int32
	if err := r.pool.QueryRow(ctx, countQ, tenantID).Scan(&total); err != nil {
		return nil, "", 0, fmt.Errorf("count tenant users: %w", err)
	}

	// FUT-012: role aggregate is computed inline via a LATERAL on
	// role_assignments. tenant_admin / platform_admin are independent
	// boolean checks because they reflect specific scope grants, not
	// counts. The CASE/SUM patterns keep the math in the planner — a
	// COUNT(*) FILTER would also work but FILTER is harder to read at
	// a glance for someone tracing the role calculus.
	q := `
		SELECT u.id,
		       u.username,
		       COALESCE(u.display_name, ''),
		       COALESCE(u.email, ''),
		       u.kind,
		       u.status,
		       u.last_login_at,
		       u.created_at,
		       COALESCE(ra.org_admin_count,  0),
		       COALESCE(ra.org_writer_count, 0),
		       COALESCE(ra.org_reader_count, 0),
		       COALESCE(ra.repo_grant_count, 0),
		       COALESCE(ra.tenant_admin,     FALSE),
		       COALESCE(ra.platform_admin,   FALSE)
		FROM   users u
		LEFT JOIN LATERAL (
		  SELECT
		    SUM(CASE WHEN ra.scope_type='org'  AND ro.name='admin'  THEN 1 ELSE 0 END)::INT AS org_admin_count,
		    SUM(CASE WHEN ra.scope_type='org'  AND ro.name='writer' THEN 1 ELSE 0 END)::INT AS org_writer_count,
		    SUM(CASE WHEN ra.scope_type='org'  AND ro.name='reader' THEN 1 ELSE 0 END)::INT AS org_reader_count,
		    SUM(CASE WHEN ra.scope_type='repo'                       THEN 1 ELSE 0 END)::INT AS repo_grant_count,
		    BOOL_OR(ra.scope_type='tenant' AND ro.name='admin')                                AS tenant_admin,
		    BOOL_OR(ra.scope_type='org'    AND ra.scope_value='*' AND ro.name='admin')         AS platform_admin
		    FROM role_assignments ra
		    JOIN roles ro ON ro.id = ra.role_id
		   WHERE ra.user_id   = u.id
		     AND ra.tenant_id = u.tenant_id
		) ra ON TRUE
		WHERE u.tenant_id = $1`

	args := []any{tenantID}
	if hasCursor {
		// Keyset pagination: stable across concurrent writes because
		// (created_at, id) is unique by virtue of id being a UUID
		// PRIMARY KEY. The strict comparison ensures the cursor row
		// isn't re-returned on the next page.
		q += ` AND (u.created_at, u.id) > ($2::timestamptz, $3::uuid)`
		args = append(args, cursorTime, cursorID)
	}
	q += ` ORDER BY u.created_at ASC, u.id ASC LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, pageSize+1) // fetch one extra so we can detect "has more"

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", 0, fmt.Errorf("list tenant users: %w", err)
	}
	defer rows.Close()

	out := make([]TenantUserSummary, 0, pageSize)
	for rows.Next() {
		var s TenantUserSummary
		if err := rows.Scan(
			&s.UserID, &s.Username, &s.DisplayName, &s.Email, &s.Kind, &s.Status,
			&s.LastLoginAt, &s.CreatedAt,
			&s.OrgAdminCount, &s.OrgWriterCount, &s.OrgReaderCount, &s.RepoGrantCount,
			&s.TenantAdmin, &s.PlatformAdmin,
		); err != nil {
			return nil, "", 0, fmt.Errorf("scan tenant user: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, fmt.Errorf("iterate tenant users: %w", err)
	}

	// "Fetch +1" cursor pattern: when we got more rows than the page
	// size, lop the extra off and emit a cursor pointing at the last
	// row we DID keep. Empty next_page_token means "no more pages".
	nextToken := ""
	if int32(len(out)) > pageSize {
		out = out[:pageSize]
		last := out[len(out)-1]
		nextToken = encodeUserPageToken(last.CreatedAt, last.UserID)
	}
	return out, nextToken, total, nil
}

// CreateInvitedUserRequest carries the validated inputs for an invite.
// The caller has already verified the email shape, generated the raw
// token (32 bytes of crypto/rand), hashed it via argon2id, and
// computed the absolute expiry. No defaults are applied here.
type CreateInvitedUserRequest struct {
	TenantID        uuid.UUID
	Username        string
	Email           string
	DisplayName     string
	InviteTokenHash string
	InviteExpiresAt time.Time
}

// CreateInvitedUser inserts a users row in status='invited'. The user
// can't log in until they redeem the invite (a separate flow that
// flips status to 'active' + sets password_hash). is_active is left
// FALSE for back-compat with readers that haven't migrated to status.
//
// Returns ErrAlreadyExists when (tenant_id, username) or
// (tenant_id, email) collide — the BFF maps that to a 409.
func (r *UserRepository) CreateInvitedUser(
	ctx context.Context,
	req CreateInvitedUserRequest,
) (*User, error) {
	// is_global_admin defaults to false on INSERT (migration 20260629000001).
	// onboarding_complete also defaults to false on INSERT (migration
	// 20260629000002); the wizard flips it once the invitee finishes setup.
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash,
		                   display_name, kind, status, is_active,
		                   invite_token_hash, invite_expires_at)
		VALUES ($1, $2, NULLIF($3, ''), '',
		        NULLIF($4, ''), 'human', 'invited', FALSE,
		        $5, $6)
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at, kind, is_global_admin,
		          onboarding_complete`

	var u User
	err := r.pool.QueryRow(ctx, q,
		req.TenantID, req.Username, req.Email, req.DisplayName,
		req.InviteTokenHash, req.InviteExpiresAt,
	).Scan(
		&u.ID, &u.TenantID, &u.Username, &u.Email, &u.DisplayName,
		&u.PasswordHash, &u.IsActive, &u.FailedLogins, &u.LockedUntil,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Kind, &u.IsGlobalAdmin,
		&u.OnboardingComplete,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create invited user: %w", err)
	}
	return &u, nil
}

// SetUserStatus flips users.status. The caller validates the
// transition (active↔disabled only; invited is reserved for
// CreateInvitedUser + the redemption flow). is_active is kept in sync
// for back-compat with the old readers; once Phase A ships and the
// follow-up migration drops is_active, this UPDATE will only touch
// status.
//
// Returns ErrNotFound when the user doesn't exist within the tenant —
// keeps the API symmetric with the rest of the repo.
func (r *UserRepository) SetUserStatus(
	ctx context.Context,
	tenantID, userID uuid.UUID,
	status string,
) error {
	const q = `
		UPDATE users
		   SET status     = $1,
		       is_active  = (CASE WHEN $1 = 'active' THEN TRUE ELSE FALSE END),
		       updated_at = now()
		 WHERE id        = $2
		   AND tenant_id = $3
		   AND status    IN ('active', 'disabled')`
	tag, err := r.pool.Exec(ctx, q, status, userID, tenantID)
	if err != nil {
		return fmt.Errorf("set user status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DisableAPIKeysForUser flips every active API key owned by the user
// to is_active=FALSE. Called as part of the SetUserDisabled flow so a
// disabled user can't continue authenticating via a stale API key.
//
// Returns the count of keys flipped so the service layer can log
// blast-radius metadata. Service-account-owned keys (where user_id is
// NULL) are NOT touched — disabling the SA's shadow user is the wrong
// way to disable an SA; that's what services/auth's
// SetServiceAccountDisabled is for.
func (r *UserRepository) DisableAPIKeysForUser(
	ctx context.Context,
	tenantID, userID uuid.UUID,
) (int64, error) {
	const q = `
		UPDATE api_keys
		   SET is_active = FALSE
		 WHERE user_id   = $1
		   AND tenant_id = $2
		   AND is_active = TRUE`
	tag, err := r.pool.Exec(ctx, q, userID, tenantID)
	if err != nil {
		return 0, fmt.Errorf("disable api keys for user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ── page_token helpers ──────────────────────────────────────────────

// encodeUserPageToken produces the opaque cursor string passed back to
// the FE between paginated calls. RFC3339Nano + "|" + UUID. Not
// base64'd because the FE treats it as opaque anyway; readable form
// helps debugging without leaking sensitive info (a created_at + UUID
// pair is no more sensitive than what the row itself exposes).
func encodeUserPageToken(t time.Time, id uuid.UUID) string {
	return t.Format(time.RFC3339Nano) + "|" + id.String()
}

func decodeUserPageToken(s string) (time.Time, uuid.UUID, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			t, err := time.Parse(time.RFC3339Nano, s[:i])
			if err != nil {
				return time.Time{}, uuid.Nil, fmt.Errorf("page_token time: %w", err)
			}
			id, err := uuid.Parse(s[i+1:])
			if err != nil {
				return time.Time{}, uuid.Nil, fmt.Errorf("page_token id: %w", err)
			}
			return t, id, nil
		}
	}
	return time.Time{}, uuid.Nil, errors.New("malformed page_token")
}

// pgx import is referenced indirectly through (*pgxpool.Pool).Query in
// the receiver; keeping the import here to make the dependency
// explicit + silence the linter when this file is compiled standalone.
var _ = pgx.ErrNoRows
