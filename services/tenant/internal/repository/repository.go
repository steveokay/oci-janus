// Package repository handles database access for the tenant service.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by repository helpers when a requested row does not
// exist. Callers should treat this as "value is zero / unset" rather than as a
// hard error. Mapped to gRPC NotFound by the handler layer where appropriate.
// Sentinel error so callers can branch on errors.Is without coupling to pgx.
var ErrNotFound = errors.New("not found")

// TenantRecord is a row from the tenants table.
// Slug is populated by 20260620000001_add_tenant_slug.sql; older code paths
// that constructed TenantRecord by hand will see an empty slug — callers that
// need the wildcard host should call NormalizeSlug on the name as a fallback.
type TenantRecord struct {
	ID        uuid.UUID
	Name      string
	Plan      string
	Slug      string
	CreatedAt time.Time
}

// slugCleanupRE matches one or more non-alphanumeric characters. Used by
// NormalizeSlug to collapse separators (spaces, underscores, punctuation)
// into a single `-`. Mirrors the regexp used in the SQL backfill so the
// two paths produce identical slugs.
var slugCleanupRE = regexp.MustCompile(`[^a-z0-9]+`)

// dashCollapseRE collapses runs of `-` left after the first pass so the
// output never contains `--`. Mirrors the second pass in the SQL backfill.
var dashCollapseRE = regexp.MustCompile(`-+`)

// NormalizeSlug converts a free-form tenant name into a DNS-safe slug. The
// algorithm matches 20260620000001_add_tenant_slug.sql exactly so the SQL
// backfill and any application-side slug derivation always agree:
//  1. Lowercase
//  2. Replace any non-[a-z0-9] run with `-`
//  3. Collapse runs of `-`
//  4. Trim leading/trailing `-`
// Returns "" if the input contains no alphanumerics — callers should fall
// back to the tenant id in that case (the migration does the same).
func NormalizeSlug(name string) string {
	lower := strings.ToLower(name)
	step1 := slugCleanupRE.ReplaceAllString(lower, "-")
	step2 := dashCollapseRE.ReplaceAllString(step1, "-")
	return strings.Trim(step2, "-")
}

// PolicyRecord is a row from tenant_policies.
type PolicyRecord struct {
	TenantID             uuid.UUID
	ScanOnPush           bool
	BlockOnSeverity      string
	AllowUnscanned       bool
	ProxyCacheEnabled    bool
	SigningRequired       bool
	ExemptRepositories   []string
	StorageQuotaBytes    int64
}

// Repository wraps the pgxpool and owns all SQL.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// CreateTenant inserts a new tenant and a default policy row. The slug is
// derived from the name via NormalizeSlug so the SQL backfill path and the
// new-tenant insert agree. An empty slug falls back to the tenant id.
func (r *Repository) CreateTenant(ctx context.Context, name, plan string) (*TenantRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	slug := NormalizeSlug(name)

	var rec TenantRecord
	// Insert with slug computed up-front; the COALESCE on id::text guarantees
	// we never violate the NOT NULL constraint on slug if NormalizeSlug
	// returns the empty string for an exotic name.
	err = tx.QueryRow(ctx,
		`INSERT INTO tenants (name, plan, slug)
		 VALUES ($1, $2, COALESCE(NULLIF($3, ''), gen_random_uuid()::text))
		 RETURNING id, name, plan, slug, created_at`,
		name, plan, slug,
	).Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.Slug, &rec.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert tenant: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO tenant_policies (tenant_id) VALUES ($1)`,
		rec.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert default policy: %w", err)
	}

	return &rec, tx.Commit(ctx)
}

// ListTenants returns up to `pageSize` tenants ordered by created_at DESC.
// When `afterCreated`+`afterID` are non-zero, only rows strictly older
// (created_at < afterCreated OR (created_at == afterCreated AND id < afterID))
// are returned — the (created_at, id) tuple is a stable cursor under inserts.
func (r *Repository) ListTenants(ctx context.Context, pageSize int32, afterCreated time.Time, afterID uuid.UUID) ([]TenantRecord, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	const q = `
		SELECT id, name, plan, slug, created_at
		FROM tenants
		WHERE ($1 = '0001-01-01T00:00:00Z'::timestamptz
		       OR (created_at, id) < ($1, $2))
		ORDER BY created_at DESC, id DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, q, afterCreated, afterID, pageSize)
	if err != nil {
		return nil, fmt.Errorf("ListTenants: %w", err)
	}
	defer rows.Close()
	var out []TenantRecord
	for rows.Next() {
		var rec TenantRecord
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.Slug, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListTenants scan: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetTenant returns a tenant by id. Includes slug post-FE-API-007.
func (r *Repository) GetTenant(ctx context.Context, tenantID uuid.UUID) (*TenantRecord, error) {
	var rec TenantRecord
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, plan, slug, created_at FROM tenants WHERE id = $1`,
		tenantID,
	).Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.Slug, &rec.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetTenant: %w", err)
	}
	return &rec, nil
}

// DeleteTenant removes a tenant (cascades to policies and domains).
func (r *Repository) DeleteTenant(ctx context.Context, tenantID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	return err
}

// UpdateTenant patches name and/or plan on an existing tenant (FE-API-029).
// `name` and `plan` are pointers so the caller can distinguish "no change"
// (nil) from "set to empty string" (non-nil pointer to ""). When `name`
// changes, the slug is recomputed via NormalizeSlug inside the same
// transaction so the wildcard host (`<slug>.<base>`) updates atomically —
// no observable state where the new name carries the old slug.
//
// Returns the updated tenant. ErrNotFound semantics: the caller observes
// pgx.ErrNoRows mapped via the standard error chain — there is no sentinel
// here because the gRPC handler maps NotFound separately.
func (r *Repository) UpdateTenant(ctx context.Context, tenantID uuid.UUID, name, plan *string) (*TenantRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// COALESCE keeps the existing column value when the parameter is NULL,
	// so callers that want to mutate only one column never trample the
	// other. Slug is recomputed inline when a new name is supplied; the
	// empty-slug fallback to the tenant id mirrors CreateTenant so the
	// wildcard host never collapses to `.<base>`.
	var rec TenantRecord
	var newSlug *string
	if name != nil {
		s := NormalizeSlug(*name)
		if s == "" {
			s = tenantID.String()
		}
		newSlug = &s
	}
	err = tx.QueryRow(ctx,
		`UPDATE tenants
		 SET    name = COALESCE($2, name),
		        slug = COALESCE($3, slug),
		        plan = COALESCE($4, plan)
		 WHERE  id = $1
		 RETURNING id, name, plan, slug, created_at`,
		tenantID, name, newSlug, plan,
	).Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.Slug, &rec.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("UpdateTenant: %w", err)
	}
	return &rec, tx.Commit(ctx)
}

// GetPolicy returns the tenant policy.
func (r *Repository) GetPolicy(ctx context.Context, tenantID uuid.UUID) (*PolicyRecord, error) {
	var rec PolicyRecord
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id, scan_on_push, block_on_severity, allow_unscanned,
		        proxy_cache_enabled, signing_required, exempt_repositories, storage_quota_bytes
		 FROM tenant_policies WHERE tenant_id = $1`,
		tenantID,
	).Scan(&rec.TenantID, &rec.ScanOnPush, &rec.BlockOnSeverity, &rec.AllowUnscanned,
		&rec.ProxyCacheEnabled, &rec.SigningRequired, &rec.ExemptRepositories, &rec.StorageQuotaBytes)
	if err != nil {
		return nil, fmt.Errorf("GetPolicy: %w", err)
	}
	return &rec, nil
}

// UpdatePolicy upserts the tenant policy row.
func (r *Repository) UpdatePolicy(ctx context.Context, p *PolicyRecord) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO tenant_policies
		    (tenant_id, scan_on_push, block_on_severity, allow_unscanned,
		     proxy_cache_enabled, signing_required, exempt_repositories, storage_quota_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		    scan_on_push = EXCLUDED.scan_on_push,
		    block_on_severity = EXCLUDED.block_on_severity,
		    allow_unscanned = EXCLUDED.allow_unscanned,
		    proxy_cache_enabled = EXCLUDED.proxy_cache_enabled,
		    signing_required = EXCLUDED.signing_required,
		    exempt_repositories = EXCLUDED.exempt_repositories,
		    storage_quota_bytes = EXCLUDED.storage_quota_bytes`,
		p.TenantID, p.ScanOnPush, p.BlockOnSeverity, p.AllowUnscanned,
		p.ProxyCacheEnabled, p.SigningRequired, p.ExemptRepositories, p.StorageQuotaBytes,
	)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// REDESIGN-001 Phase 3.1.a — deployment_metadata
// ─────────────────────────────────────────────────────────────────────────────

// GetDeploymentMetadata retrieves a deployment-scoped fact by key.
// Returns (nil, ErrNotFound) when the key has never been set — callers
// should treat this as "value is zero / unset" not as an error.
//
// REDESIGN-001 Phase 3.1.a. Used by the bootstrap CLI to check whether
// the deployment has already been bootstrapped.
func (r *Repository) GetDeploymentMetadata(ctx context.Context, key string) (json.RawMessage, error) {
	var value []byte
	err := r.pool.QueryRow(ctx,
		`SELECT value FROM deployment_metadata WHERE key = $1`, key,
	).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("GetDeploymentMetadata: %w", err)
	}
	return json.RawMessage(value), nil
}

// SetDeploymentMetadata upserts a deployment-scoped fact. Idempotent —
// repeated calls with the same key + value are no-ops aside from the
// updated_at bump. Callers requiring "create once, never overwrite"
// semantics (e.g. bootstrap_tenant_id) MUST check Get first.
//
// REDESIGN-001 Phase 3.1.a. Used by the bootstrap CLI to record the
// bootstrap tenant id.
func (r *Repository) SetDeploymentMetadata(ctx context.Context, key string, value json.RawMessage) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO deployment_metadata (key, value)
		 VALUES ($1, $2::jsonb)
		 ON CONFLICT (key) DO UPDATE
		 SET value = EXCLUDED.value, updated_at = now()`,
		key, []byte(value),
	)
	if err != nil {
		return fmt.Errorf("SetDeploymentMetadata: %w", err)
	}
	return nil
}

