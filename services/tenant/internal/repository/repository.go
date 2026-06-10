// Package repository handles database access for the tenant service.
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantRecord is a row from the tenants table.
type TenantRecord struct {
	ID        uuid.UUID
	Name      string
	Plan      string
	CreatedAt time.Time
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
	ID                 uuid.UUID
	TenantID           uuid.UUID
	Domain             string
	VerificationToken  string
	Verified           bool
	RegisteredAt       time.Time
	VerifiedAt         *time.Time
}

// Repository wraps the pgxpool and owns all SQL.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// CreateTenant inserts a new tenant and a default policy row.
func (r *Repository) CreateTenant(ctx context.Context, name, plan string) (*TenantRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var rec TenantRecord
	err = tx.QueryRow(ctx,
		`INSERT INTO tenants (name, plan) VALUES ($1, $2)
		 RETURNING id, name, plan, created_at`,
		name, plan,
	).Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.CreatedAt)
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

// GetTenant returns a tenant by id.
func (r *Repository) GetTenant(ctx context.Context, tenantID uuid.UUID) (*TenantRecord, error) {
	var rec TenantRecord
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, plan, created_at FROM tenants WHERE id = $1`,
		tenantID,
	).Scan(&rec.ID, &rec.Name, &rec.Plan, &rec.CreatedAt)
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
		 RETURNING id, tenant_id, domain, verification_token, verified, registered_at, verified_at`,
		tenantID, domain, token,
	).Scan(&rec.ID, &rec.TenantID, &rec.Domain, &rec.VerificationToken,
		&rec.Verified, &rec.RegisteredAt, &rec.VerifiedAt)
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

// MarkDomainVerified marks a domain as verified.
func (r *Repository) MarkDomainVerified(ctx context.Context, domainID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE tenant_domains SET verified = true, verified_at = now() WHERE id = $1`,
		domainID,
	)
	return err
}

// ListUnverifiedDomains returns unverified domains registered within maxAgeHours.
func (r *Repository) ListUnverifiedDomains(ctx context.Context, maxAgeHours int) ([]*DomainRecord, error) {
	cutoff := time.Now().Add(-time.Duration(maxAgeHours) * time.Hour)
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, domain, verification_token, verified, registered_at, verified_at
		 FROM tenant_domains
		 WHERE verified = false AND registered_at > $1
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
			&rec.Verified, &rec.RegisteredAt, &rec.VerifiedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
