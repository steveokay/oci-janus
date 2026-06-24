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

// CachedManifestRow is the projection ListCachedManifests returns — same
// row as ManifestRecord minus the body bytes (the list page doesn't need
// them) plus the upstream name (joined for display) and FUT-013's pull-
// tracking columns. Separated from ManifestRecord so callers on the OCI
// serve path don't accidentally pull the join + extra columns when all
// they want is the body.
type CachedManifestRow struct {
	ID           uuid.UUID
	UpstreamID   uuid.UUID
	UpstreamName string
	Image        string
	Reference    string
	Digest       string
	MediaType    string
	SizeBytes    int64
	FetchedAt    time.Time
	LastPulledAt *time.Time // nil = never pulled since FUT-013 columns added
	PullCount    int64
}

// CachedManifestRowFull is the FUT-016 detail-page projection — same
// shape as CachedManifestRow plus the manifest body bytes. Returned by
// GetCachedManifestByID so the BFF can parse the body into the layer /
// per-platform projection the dashboard renders.
//
// Kept distinct from ManifestRecord (which the OCI pull path uses)
// because this projection joins upstream_registries for the display
// name + carries the pull-tracking columns the operator wants to see.
type CachedManifestRowFull struct {
	ID           uuid.UUID
	UpstreamID   uuid.UUID
	UpstreamName string
	Image        string
	Reference    string
	Digest       string
	MediaType    string
	Body         []byte
	SizeBytes    int64
	FetchedAt    time.Time
	LastPulledAt *time.Time
	PullCount    int64
}

// CacheStatsRow is the aggregate GetCacheStats returns.
type CacheStatsRow struct {
	TotalManifests  int64
	TotalBytes      int64
	UniqueUpstreams int64
	TotalPulls      int64
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
//
// FUT-013: size_bytes is set inline from len(body) so the stats
// aggregate never has to call octet_length(body) over the whole table.
// On UPDATE we explicitly refresh size_bytes too (the body might differ
// between fetches of the same tag if upstream re-pushed).
// pull_count + last_pulled_at are NOT touched here — they're the
// RecordPull surface, and a re-cache from upstream is not a pull.
func (r *Repository) UpsertManifest(ctx context.Context, tenantID, upstreamID uuid.UUID, image, reference, digest, mediaType string, body []byte) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO proxy_manifests (tenant_id, upstream_id, image, reference, digest, media_type, body, size_bytes, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id, upstream_id, image, reference)
		DO UPDATE SET digest = EXCLUDED.digest, media_type = EXCLUDED.media_type,
		              body = EXCLUDED.body, size_bytes = EXCLUDED.size_bytes,
		              fetched_at = now()`,
		tenantID, upstreamID, image, reference, digest, mediaType, body, int64(len(body)),
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

// ── Cache visibility surface (FUT-013) ────────────────────────────────────────

// RecordPull bumps pull_count and last_pulled_at for a cache hit. Called
// from the handler's async path on every successful manifest serve from
// cache — we don't fail the client request if the bump fails, so this
// returns an error only for logging.
//
// Pull-by-tag and pull-by-digest both land on the same row (the row is
// keyed on tenant_id+upstream_id+image+reference, so the digest pull
// updates a different row than the tag pull — that's deliberate: the
// operator should see "alpine:3.20 pulled 7 times" and
// "alpine@sha256:abc... pulled 2 times" separately).
func (r *Repository) RecordPull(ctx context.Context, tenantID, upstreamID uuid.UUID, image, reference string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE proxy_manifests
		SET    pull_count = pull_count + 1,
		       last_pulled_at = now()
		WHERE  tenant_id   = $1
		  AND  upstream_id = $2
		  AND  image       = $3
		  AND  reference   = $4`,
		tenantID, upstreamID, image, reference,
	)
	return err
}

// ListCachedManifests returns the operator-visible cache page, sorted by
// fetched_at descending (most recently cached first). Pagination uses a
// keyset cursor on (fetched_at, id) — opaque to the caller, stable under
// concurrent writes.
//
// `upstreamID` zero-uuid means "all upstreams for this tenant".
// `imageContains` empty means "no substring filter".
// `afterFetched` / `afterID` form the cursor; both zero for the first page.
func (r *Repository) ListCachedManifests(
	ctx context.Context,
	tenantID uuid.UUID,
	upstreamID uuid.UUID,
	imageContains string,
	afterFetched time.Time,
	afterID uuid.UUID,
	limit int,
) ([]*CachedManifestRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	// Build the predicate inline. We can't use $5/$6 conditionally inside
	// a single SQL because the absent-filter case has different semantics
	// (no clause vs is-zero clause). Switching to query builders would
	// fight the codebase style — pgx-with-raw-SQL throughout — so we
	// branch on the filter shape explicitly.
	var (
		query string
		args  []any
	)
	// $1=tenant_id, $2=limit. Optional: $3=upstream_id (when set),
	// $N=image_contains (when set), $N+1=after_fetched, $N+2=after_id.
	args = append(args, tenantID, limit)
	query = `
		SELECT pm.id, pm.upstream_id, ur.name, pm.image, pm.reference,
		       pm.digest, pm.media_type, pm.size_bytes, pm.fetched_at,
		       pm.last_pulled_at, pm.pull_count
		FROM   proxy_manifests pm
		JOIN   upstream_registries ur ON ur.upstream_id = pm.upstream_id
		WHERE  pm.tenant_id = $1`

	if upstreamID != uuid.Nil {
		args = append(args, upstreamID)
		query += " AND pm.upstream_id = $" + itoa(len(args))
	}
	if imageContains != "" {
		args = append(args, "%"+imageContains+"%")
		query += " AND pm.image ILIKE $" + itoa(len(args))
	}
	// Keyset cursor: rows AFTER (afterFetched, afterID) in the descending
	// (fetched_at, id) order. (fetched_at < cursor) OR (fetched_at =
	// cursor AND id < cursor) is the standard keyset shape. afterFetched
	// is the zero-time on the first page, which makes the predicate
	// always-true (fetched_at < zero is false; id check applies only on
	// equal fetched_at — also false at zero) — but we still want every
	// row on page 1. So we skip the cursor clause entirely when
	// afterFetched is zero.
	if !afterFetched.IsZero() {
		args = append(args, afterFetched, afterID)
		i := len(args)
		query += " AND (pm.fetched_at, pm.id) < ($" + itoa(i-1) + ", $" + itoa(i) + ")"
	}
	query += " ORDER BY pm.fetched_at DESC, pm.id DESC LIMIT $2"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CachedManifestRow
	for rows.Next() {
		var rec CachedManifestRow
		if err := rows.Scan(
			&rec.ID, &rec.UpstreamID, &rec.UpstreamName, &rec.Image, &rec.Reference,
			&rec.Digest, &rec.MediaType, &rec.SizeBytes, &rec.FetchedAt,
			&rec.LastPulledAt, &rec.PullCount,
		); err != nil {
			return nil, err
		}
		out = append(out, &rec)
	}
	return out, rows.Err()
}

// GetCacheStats returns the page-header aggregate for a tenant. Empty
// caches return a zero-valued row, not ErrNotFound — the FE expects
// stable shape.
func (r *Repository) GetCacheStats(ctx context.Context, tenantID uuid.UUID) (*CacheStatsRow, error) {
	var rec CacheStatsRow
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(COUNT(*), 0),
		       COALESCE(SUM(size_bytes), 0),
		       COUNT(DISTINCT upstream_id),
		       COALESCE(SUM(pull_count), 0)
		FROM   proxy_manifests
		WHERE  tenant_id = $1`,
		tenantID,
	).Scan(&rec.TotalManifests, &rec.TotalBytes, &rec.UniqueUpstreams, &rec.TotalPulls)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetCachedManifestByID returns the FUT-016 detail-page projection
// for a single proxy_manifests row. ErrNotFound on miss or when the
// row belongs to a different tenant (we DO NOT leak existence across
// tenants — same posture as DeleteCachedManifestByID).
func (r *Repository) GetCachedManifestByID(ctx context.Context, tenantID, id uuid.UUID) (*CachedManifestRowFull, error) {
	var rec CachedManifestRowFull
	err := r.pool.QueryRow(ctx, `
		SELECT pm.id, pm.upstream_id, ur.name, pm.image, pm.reference,
		       pm.digest, pm.media_type, pm.body, pm.size_bytes, pm.fetched_at,
		       pm.last_pulled_at, pm.pull_count
		FROM   proxy_manifests pm
		JOIN   upstream_registries ur ON ur.upstream_id = pm.upstream_id
		WHERE  pm.id        = $1
		  AND  pm.tenant_id = $2`,
		id, tenantID,
	).Scan(
		&rec.ID, &rec.UpstreamID, &rec.UpstreamName, &rec.Image, &rec.Reference,
		&rec.Digest, &rec.MediaType, &rec.Body, &rec.SizeBytes, &rec.FetchedAt,
		&rec.LastPulledAt, &rec.PullCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// DeleteCachedManifestByID evicts a single cached manifest row. Returns
// ErrNotFound when the row doesn't exist or belongs to a different
// tenant (we DO NOT leak existence across tenants).
//
// Note: the underlying layer blobs in services/storage are NOT removed
// here — multiple cached manifests can share a layer blob, and the
// existing GC mark-sweep already refcounts them. A future "cached-blob
// LRU eviction" expansion of services/gc will handle that side (futures.md
// follow-up).
func (r *Repository) DeleteCachedManifestByID(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM proxy_manifests WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// itoa is a tiny strconv.Itoa to avoid importing strconv solely for the
// dynamic-placeholder builder in ListCachedManifests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func isDuplicate(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint"))
}
