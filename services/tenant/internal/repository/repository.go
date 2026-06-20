// Package repository handles database access for the tenant service.
package repository

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// DomainRecord is a row from tenant_domains.
type DomainRecord struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Domain            string
	VerificationToken string
	Verified          bool
	RegisteredAt      time.Time
	VerifiedAt        *time.Time
	// Notification flags — set after each admin alert is sent to avoid duplicates.
	Notified24h   bool
	Notified48h   bool
	// NextPollAfter is the earliest time the worker should retry DNS for this domain.
	// Updated on each failed poll using exponential backoff to reduce DNS churn.
	NextPollAfter time.Time
	// IsPrimary marks the verified domain that should be used as this tenant's
	// canonical registry hostname (FE-API-007). Enforced as at-most-one per
	// tenant via the partial unique index added in
	// 20260620000002_add_domain_is_primary.sql.
	IsPrimary bool
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

// ListDomainsByTenant returns every tenant_domains row for a tenant ordered
// so the primary row (if any) appears first, then verified rows by oldest
// verification, then unverified rows by registration time. Used by the
// host-selection algorithm to pick a registry hostname.
func (r *Repository) ListDomainsByTenant(ctx context.Context, tenantID uuid.UUID) ([]DomainRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, domain, verification_token, verified, registered_at, verified_at,
		        notified_24h, notified_48h, next_poll_after, is_primary
		 FROM tenant_domains
		 WHERE tenant_id = $1
		 ORDER BY is_primary DESC,
		          verified DESC,
		          verified_at NULLS LAST,
		          registered_at,
		          id`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("ListDomainsByTenant: %w", err)
	}
	defer rows.Close()
	var out []DomainRecord
	for rows.Next() {
		var rec DomainRecord
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.Domain, &rec.VerificationToken,
			&rec.Verified, &rec.RegisteredAt, &rec.VerifiedAt,
			&rec.Notified24h, &rec.Notified48h, &rec.NextPollAfter, &rec.IsPrimary); err != nil {
			return nil, fmt.Errorf("ListDomainsByTenant scan: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeleteTenant removes a tenant (cascades to policies and domains).
func (r *Repository) DeleteTenant(ctx context.Context, tenantID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	return err
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

// RegisterDomain inserts a new domain verification record and returns the token.
func (r *Repository) RegisterDomain(ctx context.Context, tenantID uuid.UUID, domain, token string) (*DomainRecord, error) {
	var rec DomainRecord
	err := r.pool.QueryRow(ctx,
		`INSERT INTO tenant_domains (tenant_id, domain, verification_token)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (domain) DO UPDATE
		    SET tenant_id = EXCLUDED.tenant_id,
		        verification_token = EXCLUDED.verification_token,
		        verified = false,
		        verified_at = NULL
		 RETURNING id, tenant_id, domain, verification_token, verified, registered_at, verified_at,
		           notified_24h, notified_48h, next_poll_after, is_primary`,
		tenantID, domain, token,
	).Scan(&rec.ID, &rec.TenantID, &rec.Domain, &rec.VerificationToken,
		&rec.Verified, &rec.RegisteredAt, &rec.VerifiedAt,
		&rec.Notified24h, &rec.Notified48h, &rec.NextPollAfter, &rec.IsPrimary)
	if err != nil {
		return nil, fmt.Errorf("RegisterDomain: %w", err)
	}
	return &rec, nil
}

// ResolveDomain looks up which tenant owns a domain. Returns ("", false) if not found or unverified.
func (r *Repository) ResolveDomain(ctx context.Context, domain string) (uuid.UUID, bool, error) {
	var tenantID uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id FROM tenant_domains WHERE domain = $1 AND verified = true`,
		domain,
	).Scan(&tenantID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("ResolveDomain: %w", err)
	}
	return tenantID, true, nil
}

// MarkDomainVerified marks a domain as verified and, when no other verified
// domain on this tenant carries is_primary=true, promotes this one to primary
// so a freshly-verified single custom domain immediately becomes the tenant's
// host. The promote step runs in the same transaction as the verify update so
// we never observe a tenant with a verified-but-no-primary state externally.
func (r *Repository) MarkDomainVerified(ctx context.Context, domainID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Capture tenant_id so the primary-check works without trusting the caller.
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx,
		`UPDATE tenant_domains
		 SET verified = true, verified_at = now()
		 WHERE id = $1
		 RETURNING tenant_id`,
		domainID,
	).Scan(&tenantID); err != nil {
		return fmt.Errorf("MarkDomainVerified: %w", err)
	}

	// Promote to primary only if no other primary exists. The partial unique
	// index will reject the update otherwise — the WHERE NOT EXISTS guard makes
	// the intent explicit and avoids a noisy constraint violation in logs.
	if _, err := tx.Exec(ctx,
		`UPDATE tenant_domains td
		 SET is_primary = true
		 WHERE td.id = $1
		   AND NOT EXISTS (
		         SELECT 1 FROM tenant_domains
		         WHERE tenant_id = $2 AND is_primary = true
		   )`,
		domainID, tenantID,
	); err != nil {
		return fmt.Errorf("MarkDomainVerified promote: %w", err)
	}

	return tx.Commit(ctx)
}

// ListUnverifiedDomains returns unverified domains that are due for a poll attempt.
// maxAgeHours is the window from registration; domains older than this are excluded
// because they will never verify (tenant has not responded). Only domains whose
// next_poll_after is in the past are returned, implementing exponential backoff.
func (r *Repository) ListUnverifiedDomains(ctx context.Context, maxAgeHours int) ([]*DomainRecord, error) {
	cutoff := time.Now().Add(-time.Duration(maxAgeHours) * time.Hour)
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, domain, verification_token, verified, registered_at, verified_at,
		        notified_24h, notified_48h, next_poll_after, is_primary
		 FROM tenant_domains
		 WHERE verified = false
		   AND registered_at > $1
		   AND next_poll_after <= now()
		 ORDER BY registered_at`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("ListUnverifiedDomains: %w", err)
	}
	defer rows.Close()

	var out []*DomainRecord
	for rows.Next() {
		rec := &DomainRecord{}
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.Domain, &rec.VerificationToken,
			&rec.Verified, &rec.RegisteredAt, &rec.VerifiedAt,
			&rec.Notified24h, &rec.Notified48h, &rec.NextPollAfter, &rec.IsPrimary); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MarkDomain24hNotified records that the 24-hour reminder has been sent for this domain.
func (r *Repository) MarkDomain24hNotified(ctx context.Context, domainID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_domains SET notified_24h = true WHERE id = $1`,
		domainID,
	)
	return err
}

// MarkDomain48hNotified records that the 48-hour failure notification has been sent.
func (r *Repository) MarkDomain48hNotified(ctx context.Context, domainID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_domains SET notified_48h = true WHERE id = $1`,
		domainID,
	)
	return err
}

// UpdateNextPollAfter sets the earliest next retry time for a domain, implementing
// exponential backoff between DNS polls.
func (r *Repository) UpdateNextPollAfter(ctx context.Context, domainID uuid.UUID, next time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_domains SET next_poll_after = $1 WHERE id = $2`,
		next, domainID,
	)
	return err
}
