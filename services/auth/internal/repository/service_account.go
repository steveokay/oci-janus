package repository

// service_account.go — ServiceAccountRepo, FE-API-048 (Task 5).
//
// ServiceAccountRepo handles all database operations on the service_accounts
// table and the shadow users that back them. Every service account has exactly
// one shadow user row (kind='service_account') whose synthetic email is
// sa+<sa_id>@internal.invalid. CreateAtomic inserts both rows in a single
// transaction so there is never an observable state where one exists without
// the other.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceAccount is the database model for a service account row.
//
// ShadowUserID references the users row whose kind='service_account'. It is
// used by the API-key validation path to resolve the owning SA from a key.
// CreatedBy is nullable because the creator's users row may be deleted (the FK
// is ON DELETE SET NULL), so callers must handle nil rather than assuming the
// creator is still present.
type ServiceAccount struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	ShadowUserID  uuid.UUID
	Name          string
	Description   string
	AllowedScopes []string
	// CreatedBy is nil when the creating user has been deleted.
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	// DisabledAt is nil while the account is active.
	DisabledAt *time.Time
}

// ServiceAccountWithStats augments ServiceAccount with live stats derived from
// the api_keys table. This is returned by List so callers can render key counts
// and last-used timestamps without a second round-trip.
type ServiceAccountWithStats struct {
	ServiceAccount
	// ActiveKeyCount is the number of is_active=true keys for this SA.
	ActiveKeyCount int32
	// LastUsedAt is the most recent last_used_at across all active keys, or nil
	// if no key has ever been used.
	LastUsedAt *time.Time
}

// CreateServiceAccountInput carries the validated data for creating a new SA.
// AllowedScopes may be nil — an empty slice is stored when nil is supplied so
// callers do not need to initialise a non-nil slice.
type CreateServiceAccountInput struct {
	TenantID      uuid.UUID
	Name          string
	Description   string
	AllowedScopes []string
	// CreatedBy is the human user who initiated the creation. Stored as the
	// service_accounts.created_by FK and NULLed automatically by PostgreSQL
	// when that user is later deleted.
	CreatedBy uuid.UUID
}

// UpdateServiceAccountInput carries the optional fields that may be changed via
// PATCH. Each pointer field is treated as "unchanged" when nil.
type UpdateServiceAccountInput struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// Name: nil = leave unchanged.
	Name *string
	// Description: nil = leave unchanged.
	Description *string
	// AllowedScopes: nil = leave unchanged; non-nil slice replaces the stored value.
	AllowedScopes *[]string
	// Disabled: nil = leave unchanged; true = set disabled_at to now(); false = clear.
	Disabled *bool
}

// ServiceAccountRepo performs database operations on the service_accounts table
// and the shadow users that back them.
type ServiceAccountRepo struct {
	// pool is a pgxpool.Pool for the registry-auth Postgres database.
	pool *pgxpool.Pool
}

// NewServiceAccountRepo constructs a ServiceAccountRepo backed by the given pool.
func NewServiceAccountRepo(pool *pgxpool.Pool) *ServiceAccountRepo {
	return &ServiceAccountRepo{pool: pool}
}

// CreateAtomic inserts a shadow user (kind='service_account') and a
// service_accounts row in a single transaction. The SA id is generated before
// either INSERT so the shadow user's synthetic email
// (sa+<sa_id>@internal.invalid) and username (sa-<prefix>) are deterministic
// from the SA id — no secondary UPDATE is needed.
//
// Returns (sa, shadowUserID, error). shadowUserID is exposed so callers can
// pass it to API-key creation without a second lookup.
//
// Returns ErrAlreadyExists when a service account with the same name already
// exists in the tenant.
func (r *ServiceAccountRepo) CreateAtomic(ctx context.Context, in CreateServiceAccountInput) (*ServiceAccount, uuid.UUID, error) {
	// Begin a transaction so both INSERTs are atomic.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("service_account create: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Generate the SA id now so the shadow user's email/username are derived from
	// it rather than from a database sequence that we'd need to read back first.
	saID := uuid.New()

	// Synthetic credentials for the shadow user. The email pattern is pinned in
	// spec §4.1 — changing it breaks the GetHumanByEmail guard in UserRepository.
	syntheticEmail := "sa+" + saID.String() + "@internal.invalid"
	// username uses the first 8 hex chars of the SA id for readability. The
	// prefix "sa-" makes it obvious in admin UI queries that this is a machine
	// account. The full UUID is globally unique so this 8-char prefix is unique
	// within any realistic tenant.
	syntheticUsername := "sa-" + saID.String()[:8]

	// 1. Insert the shadow user. password_hash is intentionally empty — SA
	//    authentication goes through API keys, never through the password path.
	//
	// NOTE: we do NOT map a unique violation here to ErrAlreadyExists. The
	// shadow user's email (sa+<uuid>@internal.invalid) and username (sa-<8hex>)
	// are derived from a freshly-generated UUID inside this function. A unique
	// violation on this INSERT can only mean a UUID birthday collision
	// (astronomically unlikely) or a caller bug — never a legitimate "name
	// already taken" condition. The raw pg error (with its constraint name) is
	// the most useful signal for debugging, so we wrap and return it directly.
	var shadowID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, email, password_hash, kind)
		VALUES ($1, $2, $3, '', 'service_account')
		RETURNING id`,
		in.TenantID, syntheticUsername, syntheticEmail,
	).Scan(&shadowID); err != nil {
		return nil, uuid.Nil, fmt.Errorf("service_account create: insert shadow user: %w", err)
	}

	// Normalise scopes so we never store a NULL array in Postgres.
	scopes := in.AllowedScopes
	if scopes == nil {
		scopes = []string{}
	}

	// 2. Insert the service_accounts row using the pre-generated saID.
	sa := &ServiceAccount{}
	if err := tx.QueryRow(ctx, `
		INSERT INTO service_accounts
		    (id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, shadow_user_id, name, description, allowed_scopes,
		          created_by, created_at, disabled_at`,
		saID, in.TenantID, shadowID, in.Name, in.Description, scopes, in.CreatedBy,
	).Scan(
		&sa.ID, &sa.TenantID, &sa.ShadowUserID, &sa.Name, &sa.Description,
		&sa.AllowedScopes, &sa.CreatedBy, &sa.CreatedAt, &sa.DisabledAt,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, uuid.Nil, ErrAlreadyExists
		}
		return nil, uuid.Nil, fmt.Errorf("service_account create: insert service_account: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, uuid.Nil, fmt.Errorf("service_account create: commit: %w", err)
	}
	return sa, shadowID, nil
}

// Get returns the service account with the given primary key.
// Returns ErrNotFound if no such row exists.
func (r *ServiceAccountRepo) Get(ctx context.Context, id uuid.UUID) (*ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, shadow_user_id, name, description, allowed_scopes,
		       created_by, created_at, disabled_at
		FROM   service_accounts
		WHERE  id = $1`

	sa := &ServiceAccount{}
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&sa.ID, &sa.TenantID, &sa.ShadowUserID, &sa.Name, &sa.Description,
		&sa.AllowedScopes, &sa.CreatedBy, &sa.CreatedAt, &sa.DisabledAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("service_account get: %w", err)
	}
	return sa, nil
}

// listSQL is the single fixed-template query for List. All variable conditions
// are expressed as typed sentinel parameters so the SQL text never changes —
// this satisfies the project convention (CLAUDE.md §11) that forbids using
// fmt.Sprintf to build SQL.
//
// Parameter bindings (always passed, regardless of which conditions are active):
//
//	$1  tenantID        — always filters by tenant
//	$2  includeDisabled — bool; when true the disabled_at IS NULL guard is skipped
//	$3  cursorAt        — timestamptz or NULL; when NULL the keyset clause is skipped
//	$4  cursorID        — uuid or nil; companion to $3
//	$5  limit           — pageSize+1 so the caller can detect a next page
const listSQL = `
	SELECT sa.id, sa.tenant_id, sa.shadow_user_id, sa.name, sa.description,
	       sa.allowed_scopes, sa.created_by, sa.created_at, sa.disabled_at,
	       COALESCE(COUNT(ak.id) FILTER (WHERE ak.is_active = true), 0)::int AS active_key_count,
	       MAX(ak.last_used_at) AS last_used_at
	FROM   service_accounts sa
	LEFT   JOIN api_keys ak ON ak.service_account_id = sa.id
	WHERE  sa.tenant_id = $1
	  AND  ($2::bool OR sa.disabled_at IS NULL)
	  AND  ($3::timestamptz IS NULL OR (sa.created_at, sa.id) < ($3::timestamptz, $4::uuid))
	GROUP  BY sa.id
	ORDER  BY sa.created_at DESC, sa.id DESC
	LIMIT  $5`

// List returns service accounts for the given tenant, ordered by
// (created_at DESC, id DESC) for stable keyset pagination.
//
// When includeDisabled is false, only accounts with disabled_at IS NULL are
// returned. When true, disabled accounts are included.
//
// pageToken encodes the last-seen (created_at, id) pair as a base64 string; an
// empty string means "start from the beginning". The returned nextToken is
// empty when there are no more pages. A malformed pageToken is treated as an
// empty token and returns the first page.
//
// The returned ServiceAccountWithStats rows include a live count of active API
// keys and the most recent last_used_at across those keys.
func (r *ServiceAccountRepo) List(
	ctx context.Context,
	tenantID uuid.UUID,
	includeDisabled bool,
	pageSize int,
	pageToken string,
) ([]ServiceAccountWithStats, string, error) {
	if pageSize <= 0 {
		pageSize = 20
	}

	// Decode keyset cursor from pageToken. A malformed token is silently treated
	// as "no cursor" so the caller gets the first page rather than an error.
	var (
		cursorAt *time.Time
		cursorID *uuid.UUID
	)
	if pageToken != "" {
		raw, err := base64.StdEncoding.DecodeString(pageToken)
		if err == nil {
			parts := strings.SplitN(string(raw), "|", 2)
			if len(parts) == 2 {
				if t, err2 := time.Parse(time.RFC3339Nano, parts[0]); err2 == nil {
					if id, err3 := uuid.Parse(parts[1]); err3 == nil {
						cursorAt = &t
						cursorID = &id
					}
				}
			}
		}
	}

	// Use a LEFT JOIN on api_keys to compute active_key_count and last_used_at
	// without a subquery so the planner can use the idx_api_keys_sa index.
	// All conditional logic is encoded as typed sentinel parameters (see listSQL
	// above) — no fmt.Sprintf is used to construct the query text.
	rows, err := r.pool.Query(ctx, listSQL,
		tenantID,      // $1
		includeDisabled, // $2 — true skips the disabled_at IS NULL guard
		cursorAt,      // $3 — nil skips the keyset clause
		cursorID,      // $4 — companion to $3
		pageSize+1,    // $5 — fetch one extra row to detect "has next page"
	)
	if err != nil {
		return nil, "", fmt.Errorf("service_account list: %w", err)
	}
	defer rows.Close()

	var results []ServiceAccountWithStats
	for rows.Next() {
		var row ServiceAccountWithStats
		if err := rows.Scan(
			&row.ID, &row.TenantID, &row.ShadowUserID, &row.Name, &row.Description,
			&row.AllowedScopes, &row.CreatedBy, &row.CreatedAt, &row.DisabledAt,
			&row.ActiveKeyCount, &row.LastUsedAt,
		); err != nil {
			return nil, "", fmt.Errorf("service_account list: scan: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("service_account list: rows: %w", err)
	}

	// Determine if there is a next page and build the next token.
	var nextToken string
	if len(results) > pageSize {
		results = results[:pageSize]
		last := results[len(results)-1]
		// Encode (created_at, id) as base64("RFC3339Nano|uuid").
		cursor := last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID.String()
		nextToken = base64.StdEncoding.EncodeToString([]byte(cursor))
	}

	return results, nextToken, nil
}

// Update applies the non-nil fields in UpdateServiceAccountInput to the row
// with the matching id and tenant_id. This double-bind prevents callers from
// updating another tenant's SA by id alone.
//
// Returns ErrNotFound when no matching row exists, ErrAlreadyExists when the
// new name collides with an existing SA in the same tenant.
func (r *ServiceAccountRepo) Update(ctx context.Context, in UpdateServiceAccountInput) (*ServiceAccount, error) {
	// Compute the (setScopes, scopes) pair so we can pass a typed bool to Postgres
	// without using NULLIF tricks — same pattern as UserRepository.UpdateProfile.
	var (
		setScopes bool
		scopesVal []string

		setDisabled    bool
		disabledAtVal  *time.Time
	)
	if in.AllowedScopes != nil {
		setScopes = true
		scopesVal = *in.AllowedScopes
		if scopesVal == nil {
			scopesVal = []string{}
		}
	}
	if in.Disabled != nil {
		setDisabled = true
		if *in.Disabled {
			now := time.Now().UTC()
			disabledAtVal = &now
		}
		// When *in.Disabled == false, disabledAtVal stays nil which clears disabled_at.
	}

	const q = `
		UPDATE service_accounts
		SET    name           = COALESCE($3, name),
		       description    = COALESCE($4, description),
		       allowed_scopes = CASE WHEN $5::bool THEN $6 ELSE allowed_scopes END,
		       disabled_at    = CASE WHEN $7::bool THEN $8 ELSE disabled_at   END
		WHERE  id = $1 AND tenant_id = $2
		RETURNING id, tenant_id, shadow_user_id, name, description, allowed_scopes,
		          created_by, created_at, disabled_at`

	sa := &ServiceAccount{}
	err := r.pool.QueryRow(ctx, q,
		in.ID, in.TenantID,
		in.Name, in.Description,
		setScopes, scopesVal,
		setDisabled, disabledAtVal,
	).Scan(
		&sa.ID, &sa.TenantID, &sa.ShadowUserID, &sa.Name, &sa.Description,
		&sa.AllowedScopes, &sa.CreatedBy, &sa.CreatedAt, &sa.DisabledAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("service_account update: %w", err)
	}
	return sa, nil
}

// Delete hard-deletes the shadow user that backs the given service account.
// The deletion cascades to the service_accounts row (via the
// shadow_user_id FK ON DELETE CASCADE) and to any api_keys rows (via the
// service_account_id FK ON DELETE CASCADE).
//
// Hard-delete is correct here because service accounts are not user accounts —
// soft-delete semantics for SAs would leave orphaned key hashes that can never
// authenticate but still consume space and confuse auditors.
//
// Returns ErrNotFound when no service_accounts row with the given id exists,
// so callers can distinguish "already deleted" from other errors.
func (r *ServiceAccountRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM users
		 WHERE id = (SELECT shadow_user_id FROM service_accounts WHERE id = $1)`,
		id)
	if err != nil {
		return fmt.Errorf("service_account delete: %w", err)
	}
	// RowsAffected is 0 when the SELECT subquery returns no rows (SA not found).
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountKeysAffectedByScopeShrink returns the number of active api_keys owned
// by the given SA whose scopes contain at least one value that is absent from
// the proposed new allowed_scopes set. Callers display this count to warn
// operators that narrowing the SA's allowed_scopes will orphan those keys.
//
// A count of 0 means the scope change is safe (no key grants more than the
// proposed set allows).
//
// A nil proposed slice is treated as an empty slice (removing all scopes),
// meaning every key with any scope is affected.
func (r *ServiceAccountRepo) CountKeysAffectedByScopeShrink(
	ctx context.Context,
	saID uuid.UUID,
	proposed []string,
) (int64, error) {
	// Guard against nil: pgx encodes a nil slice as SQL NULL, and
	// NOT (s = ANY(NULL)) evaluates to NULL (not TRUE) so the EXISTS clause
	// would match nothing and return 0 — masking real affected keys. An empty
	// non-nil slice encodes as '{}' which correctly matches no allowed scopes.
	if proposed == nil {
		proposed = []string{}
	}

	var n int64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM   api_keys
		WHERE  service_account_id = $1
		  AND  is_active = true
		  AND  EXISTS (
		           SELECT 1
		           FROM   unnest(scopes) s
		           WHERE  NOT (s = ANY($2))
		       )`,
		saID, proposed,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count keys affected by scope shrink: %w", err)
	}
	return n, nil
}
