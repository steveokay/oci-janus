// Package repository — oidc_trust.go is the CRUD repository for the
// FUT-001 federated-workload-identity trust rows.
//
// Each row pairs one workspace service account with one external OIDC
// IdP (issuer_url) and one allowed subject glob (subject_pattern). On a
// successful POST /auth/token/workload, the trust's service_account_id
// receives a short-lived RS256 registry JWT.
//
// All SQL is parameterised (CLAUDE.md §11); no string concatenation.
// Errors map to sentinels (ErrNotFound, ErrAlreadyExists) so the service
// layer can translate them to clean gRPC codes without parsing pg error
// strings.
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

// OIDCTrust is the in-memory shape of a row in oidc_trust_configs.
//
// LastUsedAt is nullable — newly-created rows have never been used by an
// exchange so the column is NULL; MarkUsed sets it to now() on every
// successful exchange. The pointer lets callers branch on "never used" vs
// "used at <timestamp>" without a sentinel-time check.
type OIDCTrust struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ServiceAccountID    uuid.UUID
	DisplayName         string
	IssuerURL           string
	Audience            string
	SubjectPattern      string
	JWKSCacheTTLSeconds int32
	CreatedAt           time.Time
	UpdatedAt           time.Time
	LastUsedAt          *time.Time
}

// OIDCTrustRepo performs database operations on the oidc_trust_configs table.
type OIDCTrustRepo struct {
	pool *pgxpool.Pool
}

// NewOIDCTrustRepo constructs an OIDCTrustRepo backed by the given pool.
func NewOIDCTrustRepo(pool *pgxpool.Pool) *OIDCTrustRepo {
	return &OIDCTrustRepo{pool: pool}
}

// Create inserts a new trust row and returns the persisted record.
//
// Returns ErrAlreadyExists when the (tenant_id, issuer_url, subject_pattern)
// tuple collides with an existing row — the UNIQUE constraint backstops a
// misconfigured admin who tries to register the same CI runner subject
// twice (which would otherwise lead to non-deterministic SA selection at
// exchange time).
func (r *OIDCTrustRepo) Create(ctx context.Context, in OIDCTrust) (*OIDCTrust, error) {
	// Default the cache TTL when the caller passes 0 so the row always has
	// a sane, queryable value. 3600s mirrors the migration default and the
	// typical IdP JWKS refresh cadence.
	if in.JWKSCacheTTLSeconds == 0 {
		in.JWKSCacheTTLSeconds = 3600
	}
	const q = `
		INSERT INTO oidc_trust_configs
		    (tenant_id, service_account_id, display_name, issuer_url,
		     audience, subject_pattern, jwks_cache_ttl_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, tenant_id, service_account_id, display_name, issuer_url,
		          audience, subject_pattern, jwks_cache_ttl_seconds,
		          created_at, updated_at, last_used_at`

	var t OIDCTrust
	err := r.pool.QueryRow(ctx, q,
		in.TenantID, in.ServiceAccountID, in.DisplayName, in.IssuerURL,
		in.Audience, in.SubjectPattern, in.JWKSCacheTTLSeconds,
	).Scan(
		&t.ID, &t.TenantID, &t.ServiceAccountID, &t.DisplayName, &t.IssuerURL,
		&t.Audience, &t.SubjectPattern, &t.JWKSCacheTTLSeconds,
		&t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create oidc trust: %w", err)
	}
	return &t, nil
}

// GetByID returns the trust row by primary key, scoped to the tenant so a
// caller cannot read another tenant's trust by id alone.
// Returns ErrNotFound if no such row exists in the given tenant.
func (r *OIDCTrustRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*OIDCTrust, error) {
	const q = `
		SELECT id, tenant_id, service_account_id, display_name, issuer_url,
		       audience, subject_pattern, jwks_cache_ttl_seconds,
		       created_at, updated_at, last_used_at
		FROM   oidc_trust_configs
		WHERE  id = $1 AND tenant_id = $2`

	var t OIDCTrust
	err := r.pool.QueryRow(ctx, q, id, tenantID).Scan(
		&t.ID, &t.TenantID, &t.ServiceAccountID, &t.DisplayName, &t.IssuerURL,
		&t.Audience, &t.SubjectPattern, &t.JWKSCacheTTLSeconds,
		&t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get oidc trust: %w", err)
	}
	return &t, nil
}

// List returns all trusts for the tenant, ordered by created_at DESC so
// newer trusts appear first in the admin UI. No pagination — realistic
// workspaces have ~10s of trusts at most.
func (r *OIDCTrustRepo) List(ctx context.Context, tenantID uuid.UUID) ([]*OIDCTrust, error) {
	const q = `
		SELECT id, tenant_id, service_account_id, display_name, issuer_url,
		       audience, subject_pattern, jwks_cache_ttl_seconds,
		       created_at, updated_at, last_used_at
		FROM   oidc_trust_configs
		WHERE  tenant_id = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list oidc trusts: %w", err)
	}
	defer rows.Close()

	var trusts []*OIDCTrust
	for rows.Next() {
		var t OIDCTrust
		if err := rows.Scan(
			&t.ID, &t.TenantID, &t.ServiceAccountID, &t.DisplayName, &t.IssuerURL,
			&t.Audience, &t.SubjectPattern, &t.JWKSCacheTTLSeconds,
			&t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt,
		); err != nil {
			return nil, fmt.Errorf("scan oidc trust: %w", err)
		}
		trusts = append(trusts, &t)
	}
	return trusts, rows.Err()
}

// ListByIssuer returns every trust that matches the given issuer URL.
// Used on the exchange hot path: given an OIDC token's iss claim, find
// every candidate trust before iterating to pick the one whose
// subject_pattern matches the token's sub claim. Unlike List, this query
// is NOT tenant-scoped — at exchange time we don't yet know which tenant
// the token belongs to (the iss is global to the IdP, not the tenant).
func (r *OIDCTrustRepo) ListByIssuer(ctx context.Context, issuerURL string) ([]*OIDCTrust, error) {
	const q = `
		SELECT id, tenant_id, service_account_id, display_name, issuer_url,
		       audience, subject_pattern, jwks_cache_ttl_seconds,
		       created_at, updated_at, last_used_at
		FROM   oidc_trust_configs
		WHERE  issuer_url = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("list oidc trusts by issuer: %w", err)
	}
	defer rows.Close()

	var trusts []*OIDCTrust
	for rows.Next() {
		var t OIDCTrust
		if err := rows.Scan(
			&t.ID, &t.TenantID, &t.ServiceAccountID, &t.DisplayName, &t.IssuerURL,
			&t.Audience, &t.SubjectPattern, &t.JWKSCacheTTLSeconds,
			&t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt,
		); err != nil {
			return nil, fmt.Errorf("scan oidc trust: %w", err)
		}
		trusts = append(trusts, &t)
	}
	return trusts, rows.Err()
}

// Update applies the mutable fields (display_name, subject_pattern,
// jwks_cache_ttl_seconds) to the row identified by (id, tenant_id).
//
// service_account_id, issuer_url, and audience are append-only — operators
// must Delete+Create to change them so the IdP-bound identity cannot be
// silently re-pointed at a different SA.
//
// Returns ErrNotFound when no matching row exists, ErrAlreadyExists when
// the new subject_pattern collides with an existing row in the same
// (tenant_id, issuer_url).
func (r *OIDCTrustRepo) Update(ctx context.Context, in OIDCTrust) (*OIDCTrust, error) {
	if in.JWKSCacheTTLSeconds == 0 {
		in.JWKSCacheTTLSeconds = 3600
	}
	const q = `
		UPDATE oidc_trust_configs
		SET    display_name           = $3,
		       subject_pattern        = $4,
		       jwks_cache_ttl_seconds = $5,
		       updated_at             = now()
		WHERE  id = $1 AND tenant_id = $2
		RETURNING id, tenant_id, service_account_id, display_name, issuer_url,
		          audience, subject_pattern, jwks_cache_ttl_seconds,
		          created_at, updated_at, last_used_at`

	var t OIDCTrust
	err := r.pool.QueryRow(ctx, q,
		in.ID, in.TenantID, in.DisplayName, in.SubjectPattern, in.JWKSCacheTTLSeconds,
	).Scan(
		&t.ID, &t.TenantID, &t.ServiceAccountID, &t.DisplayName, &t.IssuerURL,
		&t.Audience, &t.SubjectPattern, &t.JWKSCacheTTLSeconds,
		&t.CreatedAt, &t.UpdatedAt, &t.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("update oidc trust: %w", err)
	}
	return &t, nil
}

// Delete removes a trust row by (id, tenant_id). The double-bind prevents
// callers from deleting another tenant's trust by id alone.
// Returns ErrNotFound when no matching row exists.
func (r *OIDCTrustRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	const q = `DELETE FROM oidc_trust_configs WHERE id = $1 AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete oidc trust: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkUsed updates last_used_at to now() for the given trust id. Called
// on every successful ExchangeWorkloadToken so operators can see which
// trusts are actively in use. Best-effort: a failure here does NOT block
// the exchange (the caller logs + continues).
func (r *OIDCTrustRepo) MarkUsed(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE oidc_trust_configs SET last_used_at = now() WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}
