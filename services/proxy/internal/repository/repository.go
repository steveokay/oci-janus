// Package repository handles all database access for registry-proxy.
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpstreamRecord is a row from upstream_registries.
type UpstreamRecord struct {
	UpstreamID  uuid.UUID
	TenantID    uuid.UUID
	Name        string
	URL         string
	AuthType    string
	Username    string
	PasswordEnc []byte
	TTLSeconds  int64
	Enabled     bool
	CreatedAt   time.Time
}

// ManifestRecord is a row from proxy_manifests.
type ManifestRecord struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UpstreamID uuid.UUID
	Image      string
	Reference  string
	Digest     string
	MediaType  string
	Body       []byte
	FetchedAt  time.Time
}

// Repository performs all database operations for the proxy service.
type Repository struct {
	pool *pgxpool.Pool
}

// New returns a Repository backed by the given pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ── Upstream registries ───────────────────────────────────────────────────────

func (r *Repository) CreateUpstream(ctx context.Context, tenantID uuid.UUID, name, url, authType, username string, passwordEnc []byte, ttlSeconds int64) (*UpstreamRecord, error) {
	var rec UpstreamRecord
	err := r.pool.QueryRow(ctx, `
		INSERT INTO upstream_registries
		    (tenant_id, name, url, auth_type, username, password_enc, ttl_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING upstream_id, tenant_id, name, url, auth_type, username, password_enc,
		          ttl_seconds, enabled, created_at`,
		tenantID, name, url, authType, username, passwordEnc, ttlSeconds,
	).Scan(
		&rec.UpstreamID, &rec.TenantID, &rec.Name, &rec.URL, &rec.AuthType,
		&rec.Username, &rec.PasswordEnc, &rec.TTLSeconds, &rec.Enabled, &rec.CreatedAt,
	)
	if err != nil {
		if isDuplicate(err) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	return &rec, nil
}

func (r *Repository) GetUpstream(ctx context.Context, upstreamID, tenantID uuid.UUID) (*UpstreamRecord, error) {
	var rec UpstreamRecord
	err := r.pool.QueryRow(ctx, `
		SELECT upstream_id, tenant_id, name, url, auth_type, username, password_enc,
		       ttl_seconds, enabled, created_at
		FROM   upstream_registries
		WHERE  upstream_id = $1 AND tenant_id = $2`,
		upstreamID, tenantID,
	).Scan(
		&rec.UpstreamID, &rec.TenantID, &rec.Name, &rec.URL, &rec.AuthType,
		&rec.Username, &rec.PasswordEnc, &rec.TTLSeconds, &rec.Enabled, &rec.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &rec, err
}

func (r *Repository) GetUpstreamByName(ctx context.Context, tenantID uuid.UUID, name string) (*UpstreamRecord, error) {
	var rec UpstreamRecord
	err := r.pool.QueryRow(ctx, `
		SELECT upstream_id, tenant_id, name, url, auth_type, username, password_enc,
		       ttl_seconds, enabled, created_at
		FROM   upstream_registries
		WHERE  tenant_id = $1 AND name = $2 AND enabled = true`,
		tenantID, name,
	).Scan(
		&rec.UpstreamID, &rec.TenantID, &rec.Name, &rec.URL, &rec.AuthType,
		&rec.Username, &rec.PasswordEnc, &rec.TTLSeconds, &rec.Enabled, &rec.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &rec, err
}

func (r *Repository) ListUpstreams(ctx context.Context, tenantID uuid.UUID) ([]*UpstreamRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT upstream_id, tenant_id, name, url, auth_type, username, password_enc,
		       ttl_seconds, enabled, created_at
		FROM   upstream_registries
		WHERE  tenant_id = $1
		ORDER  BY name`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recs []*UpstreamRecord
	for rows.Next() {
		var rec UpstreamRecord
		if err := rows.Scan(
			&rec.UpstreamID, &rec.TenantID, &rec.Name, &rec.URL, &rec.AuthType,
			&rec.Username, &rec.PasswordEnc, &rec.TTLSeconds, &rec.Enabled, &rec.CreatedAt,
		); err != nil {
			return nil, err
		}
		recs = append(recs, &rec)
	}
	return recs, rows.Err()
}

func (r *Repository) DeleteUpstream(ctx context.Context, upstreamID, tenantID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM upstream_registries WHERE upstream_id = $1 AND tenant_id = $2`,
		upstreamID, tenantID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Manifest cache ────────────────────────────────────────────────────────────

// UpsertManifest inserts or refreshes a cached manifest entry.
func (r *Repository) UpsertManifest(ctx context.Context, tenantID, upstreamID uuid.UUID, image, reference, digest, mediaType string, body []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO proxy_manifests (tenant_id, upstream_id, image, reference, digest, media_type, body, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (tenant_id, upstream_id, image, reference)
		DO UPDATE SET digest = EXCLUDED.digest, media_type = EXCLUDED.media_type,
		              body = EXCLUDED.body, fetched_at = now()`,
		tenantID, upstreamID, image, reference, digest, mediaType, body,
	)
	return err
}

// GetManifest returns a cached manifest that is still within its TTL.
// Returns ErrNotFound if absent or stale.
func (r *Repository) GetManifest(ctx context.Context, tenantID, upstreamID uuid.UUID, image, reference string, ttlSeconds int64) (*ManifestRecord, error) {
	var rec ManifestRecord
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, upstream_id, image, reference, digest, media_type, body, fetched_at
		FROM   proxy_manifests
		WHERE  tenant_id   = $1
		  AND  upstream_id = $2
		  AND  image       = $3
		  AND  reference   = $4
		  AND  fetched_at + ($5 * interval '1 second') > now()`,
		tenantID, upstreamID, image, reference, ttlSeconds,
	).Scan(
		&rec.ID, &rec.TenantID, &rec.UpstreamID, &rec.Image,
		&rec.Reference, &rec.Digest, &rec.MediaType, &rec.Body, &rec.FetchedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &rec, err
}

func isDuplicate(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint"))
}
