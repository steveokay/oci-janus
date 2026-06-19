// Package repository contains all database access for registry-metadata.
// No SQL appears outside this package.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// Repository performs all database operations for the metadata service.
type Repository struct {
	pool     *pgxpool.Pool
	readPool *pgxpool.Pool // optional read replica; falls back to pool when nil
}

// New returns a Repository that sends all queries to pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewWithReplica returns a Repository that routes heavy list queries to readPool
// when non-nil, offloading the primary. ReadPool may be nil; the primary is used.
func NewWithReplica(pool, readPool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, readPool: readPool}
}

// reader returns the replica pool for read-only list queries when available,
// falling back to the primary pool.
func (r *Repository) reader() *pgxpool.Pool {
	if r.readPool != nil {
		return r.readPool
	}
	return r.pool
}

// ── Repositories ────────────────────────────────────────────────────────────

// repoSelectCols is the column list for every Repository row read.
// Always joined against organizations so callers receive the parent org name —
// FE-API-010 needs `org` to render `/repositories/{org}/{leaf}` links without
// a second lookup.
const repoSelectCols = `r.id, r.org_id, r.tenant_id, r.name, r.is_public,
	r.storage_quota, r.storage_used, r.created_at, o.name, r.description`

// CreateRepository inserts a new repository row.
func (r *Repository) CreateRepository(ctx context.Context, tenantID, orgID, name, description string, isPublic bool, storageQuota int64) (*metadatav1.Repository, error) {
	// Two-step insert: the RETURNING clause cannot reference the joined
	// organizations row, so we fetch the org name in the same query via a CTE.
	const q = `
		WITH inserted AS (
			INSERT INTO repositories (org_id, tenant_id, name, is_public, storage_quota, description)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, storage_used, created_at, description
		)
		SELECT r.id, r.org_id, r.tenant_id, r.name, r.is_public,
		       r.storage_quota, r.storage_used, r.created_at, o.name, r.description
		FROM inserted r
		JOIN organizations o ON o.id = r.org_id`

	quota := storageQuota
	if quota <= 0 {
		quota = 10 << 30 // 10 GiB default
	}

	repo, err := r.scanOneRepo(ctx, q, orgID, tenantID, name, isPublic, quota, description)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create repository: %w", err)
	}
	return repo, nil
}

// UpdateRepository patches mutable fields on a repository. Currently only
// description is mutable — quota has its own RPC to preserve audit intent.
func (r *Repository) UpdateRepository(ctx context.Context, tenantID, repoID, description string) (*metadatav1.Repository, error) {
	const q = `
		UPDATE repositories r
		SET    description = $1
		FROM   organizations o
		WHERE  r.id        = $2
		  AND  r.tenant_id = $3
		  AND  o.id        = r.org_id
		RETURNING r.id, r.org_id, r.tenant_id, r.name, r.is_public,
		          r.storage_quota, r.storage_used, r.created_at, o.name, r.description`
	repo, err := r.scanOneRepo(ctx, q, description, repoID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("update repository: %w", err)
	}
	return repo, nil
}

// GetRepository returns a repository by repo_id, enforcing tenant isolation.
func (r *Repository) GetRepository(ctx context.Context, tenantID, repoID string) (*metadatav1.Repository, error) {
	const q = `
		SELECT ` + repoSelectCols + `
		FROM repositories r
		JOIN organizations o ON o.id = r.org_id
		WHERE  r.id = $1 AND r.tenant_id = $2`
	return r.scanOneRepo(ctx, q, repoID, tenantID)
}

// GetRepositoryByName looks up a repository by org+name within a tenant.
func (r *Repository) GetRepositoryByName(ctx context.Context, tenantID, orgID, name string) (*metadatav1.Repository, error) {
	const q = `
		SELECT ` + repoSelectCols + `
		FROM repositories r
		JOIN organizations o ON o.id = r.org_id
		WHERE  r.org_id = $1 AND r.name = $2 AND r.tenant_id = $3`
	return r.scanOneRepo(ctx, q, orgID, name, tenantID)
}

// GetRepositoryByFullName looks up a repository by its combined "org/repo" full name within a tenant.
// The SQL JOIN avoids an application-side split and keeps the query parameterised (CLAUDE.md §13).
func (r *Repository) GetRepositoryByFullName(ctx context.Context, tenantID, fullName string) (*metadatav1.Repository, error) {
	const q = `
		SELECT ` + repoSelectCols + `
		FROM repositories r
		JOIN organizations o ON o.id = r.org_id
		WHERE r.tenant_id = $1 AND o.name || '/' || r.name = $2
		LIMIT 1`
	return r.scanOneRepo(ctx, q, tenantID, fullName)
}

// ListRepositories returns all repositories for the given tenant (+ optional org filter).
func (r *Repository) ListRepositories(ctx context.Context, tenantID, orgID string) ([]*metadatav1.Repository, error) {
	var (
		q    string
		args []any
	)
	if orgID != "" {
		q = `SELECT ` + repoSelectCols + `
		     FROM repositories r
		     JOIN organizations o ON o.id = r.org_id
		     WHERE r.tenant_id = $1 AND r.org_id = $2 ORDER BY r.name`
		args = []any{tenantID, orgID}
	} else {
		q = `SELECT ` + repoSelectCols + `
		     FROM repositories r
		     JOIN organizations o ON o.id = r.org_id
		     WHERE r.tenant_id = $1 ORDER BY r.name`
		args = []any{tenantID}
	}

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()

	var repos []*metadatav1.Repository
	for rows.Next() {
		var repo metadatav1.Repository
		var createdAt time.Time
		if err := rows.Scan(&repo.RepoId, &repo.OrgId, &repo.TenantId, &repo.Name,
			&repo.IsPublic, &repo.StorageQuota, &repo.StorageUsed, &createdAt, &repo.Org); err != nil {
			return nil, fmt.Errorf("scan repository: %w", err)
		}
		repo.CreatedAt = timestamppb.New(createdAt)
		repos = append(repos, &repo)
	}
	return repos, rows.Err()
}

// DeleteRepository removes a repository row (cascades to tags, manifests, blob_links).
func (r *Repository) DeleteRepository(ctx context.Context, tenantID, repoID string) error {
	const q = `DELETE FROM repositories WHERE id = $1 AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, repoID, tenantID)
	if err != nil {
		return fmt.Errorf("delete repository: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRepositoryQuota sets storage_quota for a repository.
func (r *Repository) UpdateRepositoryQuota(ctx context.Context, tenantID, repoID string, quota int64) (*metadatav1.Repository, error) {
	// As with CreateRepository, RETURNING can't reach the joined org row;
	// CTE-then-join to surface the parent org name in a single round trip.
	const q = `
		WITH updated AS (
			UPDATE repositories SET storage_quota = $1
			WHERE  id = $2 AND tenant_id = $3
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, storage_used, created_at, description
		)
		SELECT r.id, r.org_id, r.tenant_id, r.name, r.is_public,
		       r.storage_quota, r.storage_used, r.created_at, o.name, r.description
		FROM updated r
		JOIN organizations o ON o.id = r.org_id`
	return r.scanOneRepo(ctx, q, quota, repoID, tenantID)
}

func (r *Repository) scanOneRepo(ctx context.Context, query string, args ...any) (*metadatav1.Repository, error) {
	var repo metadatav1.Repository
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&repo.RepoId, &repo.OrgId, &repo.TenantId, &repo.Name,
		&repo.IsPublic, &repo.StorageQuota, &repo.StorageUsed, &createdAt, &repo.Org,
		&repo.Description,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	repo.CreatedAt = timestamppb.New(createdAt)
	return &repo, nil
}

// GetOrCreateOrganization returns the org with the given name for a tenant, creating it if absent.
func (r *Repository) GetOrCreateOrganization(ctx context.Context, tenantID, orgName string) (orgID string, err error) {
	const q = `
		INSERT INTO organizations (tenant_id, name)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`
	err = r.pool.QueryRow(ctx, q, tenantID, orgName).Scan(&orgID)
	if err != nil {
		return "", fmt.Errorf("get or create organization: %w", err)
	}
	return orgID, nil
}

// ── Tags ────────────────────────────────────────────────────────────────────

// tagSelectCols is the column list for every Tag row read.
// LEFT JOIN against `manifests` keeps the row when the referenced manifest is
// missing (transient state during deletes) and returns size_bytes=0 in that
// case instead of dropping the tag from the result set.
const tagSelectCols = `t.id, t.repo_id, t.tenant_id, t.name, t.manifest_digest,
	t.updated_at, t.created_at, COALESCE(m.image_size_bytes, 0)`

const tagFromJoin = `FROM tags t
	LEFT JOIN manifests m
	  ON  m.repo_id   = t.repo_id
	  AND m.tenant_id = t.tenant_id
	  AND m.digest    = t.manifest_digest`

// PutTag upserts a tag (insert or update manifest_digest + updated_at).
func (r *Repository) PutTag(ctx context.Context, tenantID, repoID, name, manifestDigest string) (*metadatav1.Tag, error) {
	// CTE-then-join: RETURNING from the upsert can't reach the joined
	// manifests row, so wrap the INSERT and join the size in a second SELECT.
	const q = `
		WITH upserted AS (
			INSERT INTO tags (repo_id, tenant_id, name, manifest_digest)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (repo_id, name) DO UPDATE
			  SET manifest_digest = EXCLUDED.manifest_digest, updated_at = now()
			RETURNING id, repo_id, tenant_id, name, manifest_digest, updated_at, created_at
		)
		SELECT t.id, t.repo_id, t.tenant_id, t.name, t.manifest_digest,
		       t.updated_at, t.created_at, COALESCE(m.image_size_bytes, 0)
		FROM upserted t
		LEFT JOIN manifests m
		  ON  m.repo_id   = t.repo_id
		  AND m.tenant_id = t.tenant_id
		  AND m.digest    = t.manifest_digest`
	return r.scanOneTag(ctx, q, repoID, tenantID, name, manifestDigest)
}

// GetTag returns a tag by repo_id + name, enforcing tenant isolation.
func (r *Repository) GetTag(ctx context.Context, tenantID, repoID, name string) (*metadatav1.Tag, error) {
	const q = `
		SELECT ` + tagSelectCols + `
		` + tagFromJoin + `
		WHERE  t.repo_id = $1 AND t.name = $2 AND t.tenant_id = $3`
	return r.scanOneTag(ctx, q, repoID, name, tenantID)
}

// ListTags returns tags for a repository with cursor-based pagination.
// pageToken is the tag name to start after; pageSize 0 means no limit.
func (r *Repository) ListTags(ctx context.Context, tenantID, repoID string, pageSize int32, last string) ([]*metadatav1.Tag, error) {
	var (
		q    string
		args []any
	)
	if last != "" {
		q = `SELECT ` + tagSelectCols + `
		     ` + tagFromJoin + `
		     WHERE t.repo_id = $1 AND t.tenant_id = $2 AND t.name > $3 ORDER BY t.name`
		args = []any{repoID, tenantID, last}
	} else {
		q = `SELECT ` + tagSelectCols + `
		     ` + tagFromJoin + `
		     WHERE t.repo_id = $1 AND t.tenant_id = $2 ORDER BY t.name`
		args = []any{repoID, tenantID}
	}
	if pageSize > 0 {
		q += fmt.Sprintf(" LIMIT %d", pageSize)
	}

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []*metadatav1.Tag
	for rows.Next() {
		var tag metadatav1.Tag
		var updatedAt, createdAt time.Time
		if err := rows.Scan(&tag.TagId, &tag.RepoId, &tag.TenantId, &tag.Name,
			&tag.ManifestDigest, &updatedAt, &createdAt, &tag.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tag.UpdatedAt = timestamppb.New(updatedAt)
		tag.CreatedAt = timestamppb.New(createdAt)
		tags = append(tags, &tag)
	}
	return tags, rows.Err()
}

// DeleteTag removes a tag row.
func (r *Repository) DeleteTag(ctx context.Context, tenantID, repoID, name string) error {
	const q = `DELETE FROM tags WHERE repo_id = $1 AND name = $2 AND tenant_id = $3`
	tag, err := r.pool.Exec(ctx, q, repoID, name, tenantID)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) scanOneTag(ctx context.Context, query string, args ...any) (*metadatav1.Tag, error) {
	var tag metadatav1.Tag
	var updatedAt, createdAt time.Time
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&tag.TagId, &tag.RepoId, &tag.TenantId, &tag.Name,
		&tag.ManifestDigest, &updatedAt, &createdAt, &tag.SizeBytes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	tag.UpdatedAt = timestamppb.New(updatedAt)
	tag.CreatedAt = timestamppb.New(createdAt)
	return &tag, nil
}

// ── Manifests ───────────────────────────────────────────────────────────────

// PutManifest upserts a manifest row.
//
// `size_bytes` is the on-the-wire size of the manifest document itself; the
// aggregate image size (config blob + sum of layer blob sizes, or for an
// index, the sum of child manifest sizes) is parsed from rawJSON via
// parseImageSize and stored in `image_size_bytes` so the tag-level size can
// be returned in O(1) without re-parsing on every read.
func (r *Repository) PutManifest(ctx context.Context, tenantID, repoID, digest, mediaType string, rawJSON []byte, sizeBytes int64) (*metadatav1.Manifest, error) {
	const q = `
		INSERT INTO manifests (repo_id, tenant_id, digest, media_type, raw_json, size_bytes, image_size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (repo_id, digest) DO UPDATE
		  SET media_type        = EXCLUDED.media_type,
		      raw_json          = EXCLUDED.raw_json,
		      size_bytes        = EXCLUDED.size_bytes,
		      image_size_bytes  = EXCLUDED.image_size_bytes
		RETURNING id, repo_id, tenant_id, digest, media_type, raw_json, size_bytes, created_at`
	return r.scanOneManifest(ctx, q, repoID, tenantID, digest, mediaType, rawJSON, sizeBytes, parseImageSize(rawJSON))
}

// parseImageSize derives the total image size in bytes from an OCI manifest's
// raw JSON. For an image manifest, the size is config.size + sum(layers[].size).
// For an image index, the index document doesn't reference blob sizes directly,
// so the size is sum(manifests[].size) — the cumulative size of the per-platform
// manifest documents the index points to. Returns 0 when the JSON cannot be
// parsed or has no recognised structure; the caller stores 0 and any future
// reader treats it as "unknown".
// maxManifestEntries caps the number of layers/manifests we iterate over when
// computing image size. A legitimate OCI image rarely exceeds ~200 layers; 1000
// is a generous ceiling that prevents a crafted document from consuming CPU in a
// tight summation loop.
const maxManifestEntries = 1000

func parseImageSize(rawJSON []byte) int64 {
	if len(rawJSON) == 0 {
		return 0
	}
	var doc struct {
		Config    *struct{ Size int64 `json:"size"` } `json:"config"`
		Layers    []struct{ Size int64 `json:"size"` } `json:"layers"`
		Manifests []struct{ Size int64 `json:"size"` } `json:"manifests"`
	}
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		return 0
	}
	layers := doc.Layers
	if len(layers) > maxManifestEntries {
		layers = layers[:maxManifestEntries]
	}
	manifests := doc.Manifests
	if len(manifests) > maxManifestEntries {
		manifests = manifests[:maxManifestEntries]
	}
	var total int64
	if doc.Config != nil {
		total += doc.Config.Size
	}
	for _, l := range layers {
		total += l.Size
	}
	// Image index path: no layers, sum child manifest doc sizes instead.
	if len(layers) == 0 {
		for _, m := range manifests {
			total += m.Size
		}
	}
	return total
}

// GetManifest resolves a reference (digest or tag name) to a manifest.
func (r *Repository) GetManifest(ctx context.Context, tenantID, repoID, reference string) (*metadatav1.Manifest, error) {
	digest := reference
	// If the reference is a tag name (not a digest), resolve it first.
	if !strings.HasPrefix(reference, "sha256:") {
		tag, err := r.GetTag(ctx, tenantID, repoID, reference)
		if err != nil {
			return nil, err
		}
		digest = tag.ManifestDigest
	}

	const q = `
		SELECT id, repo_id, tenant_id, digest, media_type, raw_json, size_bytes, created_at
		FROM   manifests
		WHERE  repo_id = $1 AND digest = $2 AND tenant_id = $3`
	return r.scanOneManifest(ctx, q, repoID, digest, tenantID)
}

// DeleteManifest removes a manifest row.
func (r *Repository) DeleteManifest(ctx context.Context, tenantID, repoID, digest string) error {
	const q = `DELETE FROM manifests WHERE repo_id = $1 AND digest = $2 AND tenant_id = $3`
	tag, err := r.pool.Exec(ctx, q, repoID, digest, tenantID)
	if err != nil {
		return fmt.Errorf("delete manifest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUntaggedManifests returns manifests in a repo that have no tag pointing to them.
func (r *Repository) ListUntaggedManifests(ctx context.Context, tenantID, repoID string) ([]*metadatav1.Manifest, error) {
	const q = `
		SELECT m.id, m.repo_id, m.tenant_id, m.digest, m.media_type, m.raw_json, m.size_bytes, m.created_at
		FROM   manifests m
		WHERE  m.repo_id = $1 AND m.tenant_id = $2
		  AND  NOT EXISTS (
		           SELECT 1 FROM tags t
		           WHERE  t.repo_id = m.repo_id AND t.manifest_digest = m.digest
		       )`

	rows, err := r.pool.Query(ctx, q, repoID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list untagged manifests: %w", err)
	}
	defer rows.Close()

	var manifests []*metadatav1.Manifest
	for rows.Next() {
		var m metadatav1.Manifest
		var createdAt time.Time
		if err := rows.Scan(&m.ManifestId, &m.RepoId, &m.TenantId, &m.Digest,
			&m.MediaType, &m.RawJson, &m.SizeBytes, &createdAt); err != nil {
			return nil, fmt.Errorf("scan manifest: %w", err)
		}
		m.CreatedAt = timestamppb.New(createdAt)
		manifests = append(manifests, &m)
	}
	return manifests, rows.Err()
}

func (r *Repository) scanOneManifest(ctx context.Context, query string, args ...any) (*metadatav1.Manifest, error) {
	var m metadatav1.Manifest
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&m.ManifestId, &m.RepoId, &m.TenantId, &m.Digest,
		&m.MediaType, &m.RawJson, &m.SizeBytes, &createdAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.CreatedAt = timestamppb.New(createdAt)
	return &m, nil
}

// ── Blobs ────────────────────────────────────────────────────────────────────

// LinkBlob ensures the blob row exists in `blobs` then inserts into `blob_links`.
func (r *Repository) LinkBlob(ctx context.Context, repoID, digest, storageKey string, sizeBytes int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Upsert the canonical blob record (deduplication).
	const upsertBlob = `
		INSERT INTO blobs (digest, size_bytes, storage_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (digest) DO NOTHING`
	if _, err := tx.Exec(ctx, upsertBlob, digest, sizeBytes, storageKey); err != nil {
		return fmt.Errorf("upsert blob: %w", err)
	}

	// Link the blob to this repository.
	const insertLink = `
		INSERT INTO blob_links (repo_id, blob_digest)
		VALUES ($1, $2)
		ON CONFLICT (repo_id, blob_digest) DO NOTHING`
	if _, err := tx.Exec(ctx, insertLink, repoID, digest); err != nil {
		return fmt.Errorf("insert blob link: %w", err)
	}

	return tx.Commit(ctx)
}

// UnlinkBlob removes the blob_links row for (repo_id, digest).
func (r *Repository) UnlinkBlob(ctx context.Context, repoID, digest string) error {
	const q = `DELETE FROM blob_links WHERE repo_id = $1 AND blob_digest = $2`
	tag, err := r.pool.Exec(ctx, q, repoID, digest)
	if err != nil {
		return fmt.Errorf("unlink blob: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListOrphanedBlobs returns blobs that have no blob_links referencing them.
// These are safe to delete during GC (subject to min-age checks in the GC service).
func (r *Repository) ListOrphanedBlobs(ctx context.Context) ([]*metadatav1.BlobRef, error) {
	const q = `
		SELECT b.digest, b.size_bytes, b.storage_key
		FROM   blobs b
		WHERE  NOT EXISTS (
		           SELECT 1 FROM blob_links bl WHERE bl.blob_digest = b.digest
		       )`

	rows, err := r.reader().Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list orphaned blobs: %w", err)
	}
	defer rows.Close()

	var blobs []*metadatav1.BlobRef
	for rows.Next() {
		var b metadatav1.BlobRef
		if err := rows.Scan(&b.Digest, &b.SizeBytes, &b.StorageKey); err != nil {
			return nil, fmt.Errorf("scan blob ref: %w", err)
		}
		blobs = append(blobs, &b)
	}
	return blobs, rows.Err()
}

// ── Quota ────────────────────────────────────────────────────────────────────

// GetTenantQuotaUsage returns the tenant's current used bytes (summed across its
// repositories) and its tenant-level quota cap (tenants.storage_quota). Aggregating
// quotas across repos was misleading because adding a new repo would inflate the
// total cap without any admin action — tenant-level quota is the canonical model
// and is bumped per customer via UpdateTenantQuota.
func (r *Repository) GetTenantQuotaUsage(ctx context.Context, tenantID string) (*metadatav1.QuotaUsage, error) {
	const q = `
		SELECT
		    COALESCE((SELECT SUM(storage_used) FROM repositories WHERE tenant_id = $1), 0) AS used_bytes,
		    t.storage_quota                                                                AS quota_bytes
		FROM   tenants t
		WHERE  t.id = $1`

	var usage metadatav1.QuotaUsage
	usage.TenantId = tenantID
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&usage.UsedBytes, &usage.QuotaBytes); err != nil {
		return nil, fmt.Errorf("get quota usage: %w", err)
	}
	return &usage, nil
}

// UpdateTenantQuota sets the tenant-level storage_quota. Used by the management
// API's super-admin quota route to bump quotas for large customers.
func (r *Repository) UpdateTenantQuota(ctx context.Context, tenantID string, quotaBytes int64) (*metadatav1.QuotaUsage, error) {
	const q = `UPDATE tenants SET storage_quota = $2 WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, tenantID, quotaBytes)
	if err != nil {
		return nil, fmt.Errorf("update tenant quota: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.GetTenantQuotaUsage(ctx, tenantID)
}

// IncrementTenantStorage adds bytes to storage_used for a specific repo (and by extension the tenant).
func (r *Repository) IncrementTenantStorage(ctx context.Context, tenantID string, bytes int64) error {
	// Increment storage_used across all repos for the tenant proportionally is complex.
	// The canonical approach: track at tenant level via a separate counter or sum.
	// For simplicity: update all repos for the tenant by adding to the first (or use a dedicated tenant quota table).
	// Per schema, storage_used is per-repository. This RPC increments the aggregate by
	// recording bytes against a synthetic "default" repo or by updating the tenant-wide sum.
	// Practical implementation: update storage_used on a repo specified via context
	// (callers pass repoID in practice; this method accepts tenantID and adds to the total).
	// We store usage per tenant across all repos — use a single UPDATE that adds bytes
	// proportionally (this is an approximation; the GC service recomputes exact values).
	const q = `
		UPDATE repositories
		SET    storage_used = storage_used + $1
		WHERE  tenant_id = $2
		  AND  id = (SELECT id FROM repositories WHERE tenant_id = $2 ORDER BY created_at LIMIT 1)`
	_, err := r.pool.Exec(ctx, q, bytes, tenantID)
	return err
}

// DecrementTenantStorage subtracts bytes from storage_used.
func (r *Repository) DecrementTenantStorage(ctx context.Context, tenantID string, bytes int64) error {
	const q = `
		UPDATE repositories
		SET    storage_used = GREATEST(0, storage_used - $1)
		WHERE  tenant_id = $2
		  AND  id = (SELECT id FROM repositories WHERE tenant_id = $2 ORDER BY created_at LIMIT 1)`
	_, err := r.pool.Exec(ctx, q, bytes, tenantID)
	return err
}

// IncrementRepoStorage adds bytes to storage_used for the named repository.
func (r *Repository) IncrementRepoStorage(ctx context.Context, tenantID, repoID string, bytes int64) error {
	const q = `UPDATE repositories SET storage_used = storage_used + $1 WHERE id = $2 AND tenant_id = $3`
	_, err := r.pool.Exec(ctx, q, bytes, repoID, tenantID)
	return err
}

// DecrementRepoStorage subtracts bytes from storage_used for the named repository.
func (r *Repository) DecrementRepoStorage(ctx context.Context, tenantID, repoID string, bytes int64) error {
	const q = `UPDATE repositories SET storage_used = GREATEST(0, storage_used - $1) WHERE id = $2 AND tenant_id = $3`
	_, err := r.pool.Exec(ctx, q, bytes, repoID, tenantID)
	return err
}

// ── Scan results ─────────────────────────────────────────────────────────────

// UpsertScanResult inserts or updates a scan_results row identified by scan_id.
func (r *Repository) UpsertScanResult(ctx context.Context, scanID, tenantID, status string, findingsJSON []byte, severityCounts map[string]int32) error {
	countsJSON, err := json.Marshal(severityCounts)
	if err != nil {
		return fmt.Errorf("marshal severity counts: %w", err)
	}

	var startedAt, completedAt *time.Time
	now := time.Now()
	switch status {
	case "running":
		startedAt = &now
	case "complete", "failed":
		completedAt = &now
	}

	const q = `
		UPDATE scan_results
		SET    status          = $1,
		       findings        = $2,
		       severity_counts = $3,
		       started_at      = COALESCE(started_at, $4),
		       completed_at    = $5
		WHERE  id = $6 AND tenant_id = $7`

	tag, err := r.pool.Exec(ctx, q, status, findingsJSON, countsJSON, startedAt, completedAt, scanID, tenantID)
	if err != nil {
		return fmt.Errorf("update scan result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreatePendingScanResult inserts a new scan_results row with status=pending.
func (r *Repository) CreatePendingScanResult(ctx context.Context, tenantID, repoID, manifestDigest, scannerName, scannerVersion string) (string, error) {
	id := uuid.New().String()
	const q = `
		INSERT INTO scan_results (id, manifest_digest, repo_id, tenant_id, scanner_name, scanner_version, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')`
	if _, err := r.pool.Exec(ctx, q, id, manifestDigest, repoID, tenantID, scannerName, scannerVersion); err != nil {
		return "", fmt.Errorf("create scan result: %w", err)
	}
	return id, nil
}

// GetScanResult returns the latest scan result for a manifest digest in a tenant.
func (r *Repository) GetScanResult(ctx context.Context, tenantID, manifestDigest string) (*metadatav1.ScanResult, error) {
	const q = `
		SELECT id, manifest_digest, repo_id, tenant_id, scanner_name, scanner_version,
		       status, severity_counts, findings, started_at, completed_at
		FROM   scan_results
		WHERE  manifest_digest = $1 AND tenant_id = $2
		ORDER  BY created_at DESC
		LIMIT  1`

	var sr metadatav1.ScanResult
	var severityJSON, findingsJSON []byte
	var startedAt, completedAt *time.Time

	err := r.pool.QueryRow(ctx, q, manifestDigest, tenantID).Scan(
		&sr.ScanId, &sr.ManifestDigest, &sr.RepoId, &sr.TenantId,
		&sr.ScannerName, &sr.ScannerVersion, &sr.Status,
		&severityJSON, &findingsJSON, &startedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get scan result: %w", err)
	}

	if len(severityJSON) > 0 {
		if err := json.Unmarshal(severityJSON, &sr.SeverityCounts); err != nil {
			return nil, fmt.Errorf("unmarshal severity counts: %w", err)
		}
	}
	sr.FindingsJson = findingsJSON

	if startedAt != nil {
		sr.StartedAt = timestamppb.New(*startedAt)
	}
	if completedAt != nil {
		sr.CompletedAt = timestamppb.New(*completedAt)
	}

	return &sr, nil
}

// GetTenantVulnerabilityCount aggregates CRITICAL and HIGH vulnerability counts
// across all completed scan_results for a tenant. The total is the sum of both.
func (r *Repository) CountRepositories(ctx context.Context, tenantID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM repositories WHERE tenant_id = $1`
	var n int64
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("CountRepositories: %w", err)
	}
	return n, nil
}

func (r *Repository) GetTenantVulnerabilityCount(ctx context.Context, tenantID string) (total, critical, high, medium, low, negligible int64, err error) {
	const q = `
		SELECT
		  COALESCE(SUM((severity_counts->>'CRITICAL')::int),   0),
		  COALESCE(SUM((severity_counts->>'HIGH')::int),       0),
		  COALESCE(SUM((severity_counts->>'MEDIUM')::int),     0),
		  COALESCE(SUM((severity_counts->>'LOW')::int),        0),
		  COALESCE(SUM((severity_counts->>'NEGLIGIBLE')::int), 0)
		FROM scan_results
		WHERE tenant_id = $1
		  AND status    = 'complete'`

	if err = r.pool.QueryRow(ctx, q, tenantID).Scan(&critical, &high, &medium, &low, &negligible); err != nil {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("GetTenantVulnerabilityCount: %w", err)
	}
	return critical + high + medium + low + negligible, critical, high, medium, low, negligible, nil
}
