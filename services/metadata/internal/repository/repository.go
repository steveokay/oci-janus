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
//
// S-MAINT-1 B3 (2026-06-22): storage_used is computed from manifests rather
// than read from the column on `repositories`. The column is never updated
// in production — `IncrementRepoStorage` / `IncrementTenantStorage` RPCs
// exist on the proto but `services/core` (the push path) does not call
// either of them, so `repositories.storage_used` is stuck at 0. The
// per-repo storage meter and the tenant storage breakdown both depended
// on that column and rendered 0% for every repo as a result. Computing on
// read removes the drift class entirely. Cost is one indexed subquery on
// `manifests(repo_id)` per row — cheap for typical repo sizes.
const repoSelectCols = `r.id, r.org_id, r.tenant_id, r.name, r.is_public,
	r.storage_quota,
	COALESCE((SELECT SUM(image_size_bytes) FROM manifests WHERE repo_id = r.id), 0) AS storage_used,
	r.created_at, o.name, r.description,
	r.immutable_tags, r.require_signature`

// CreateRepository inserts a new repository row.
func (r *Repository) CreateRepository(ctx context.Context, tenantID, orgID, name, description string, isPublic bool, storageQuota int64) (*metadatav1.Repository, error) {
	// Two-step insert: the RETURNING clause cannot reference the joined
	// organizations row, so we fetch the org name in the same query via a CTE.
	//
	// storage_used reads through the same computed expression as repoSelectCols
	// — a brand-new repo has no manifests so the SUM is 0, but using the same
	// shape here keeps the contract symmetrical with subsequent reads.
	const q = `
		WITH inserted AS (
			INSERT INTO repositories (org_id, tenant_id, name, is_public, storage_quota, description)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, created_at, description, immutable_tags, require_signature
		)
		SELECT ` + repoSelectCols + `
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
	// CTE-then-select so RETURNING doesn't have to reach the manifests
	// subquery in repoSelectCols. The UPDATE materialises the new row;
	// the outer SELECT applies the standard read shape so storage_used
	// stays consistent with GetRepository's response.
	const q = `
		WITH updated AS (
			UPDATE repositories
			SET    description = $1
			WHERE  id = $2 AND tenant_id = $3
			RETURNING id, org_id, tenant_id, name, is_public,
			          storage_quota, created_at, description, immutable_tags, require_signature
		)
		SELECT ` + repoSelectCols + `
		FROM   updated r
		JOIN   organizations o ON o.id = r.org_id`
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

// ListRepositories returns all repositories for the given tenant (+ optional
// org filter + optional artifact_type filter).
//
// artifactType is a stable discriminator from deriveArtifactType — passing
// "helm" returns only repositories that hold at least one Helm chart
// manifest; "image" returns only those with at least one container image;
// "other" returns those whose manifests don't match any known category.
// Empty artifactType disables the filter and returns every repository in
// the tenant.
//
// The filter is implemented as an EXISTS subquery against manifests so a
// repository that holds a mix (e.g. image + chart on the same /v2/...
// namespace) is correctly returned for any matching filter value rather
// than being attributed to a single primary category. Repositories with
// no manifests at all are excluded when artifactType is non-empty.
func (r *Repository) ListRepositories(ctx context.Context, tenantID, orgID, artifactType string) ([]*metadatav1.Repository, error) {
	// Positional-param accounting: tenant_id is always $1. org_id (when
	// set) is $2. The artifact_type filter's media-type array sits at the
	// next available slot — $3 with org_id, $2 without. Building the
	// arg slice + placeholder index together keeps the two in sync.
	args := []any{tenantID}
	nextPlaceholder := 2
	whereOrg := ""
	if orgID != "" {
		args = append(args, orgID)
		whereOrg = " AND r.org_id = $2"
		nextPlaceholder = 3
	}

	filterClause := ""
	if artifactType != "" {
		// "other" can't be IN-listed — it's the negation of every known
		// media type, plus the explicit empty-string row (legacy manifests
		// pushed before the config_media_type column existed).
		if artifactType == "other" {
			args = append(args, allKnownConfigMediaTypes())
			filterClause = fmt.Sprintf(`
			   AND EXISTS (
			       SELECT 1 FROM manifests m
			       WHERE m.repo_id = r.id
			         AND m.tenant_id = r.tenant_id
			         AND (m.config_media_type = ''
			              OR NOT (m.config_media_type = ANY($%d)))
			   )`, nextPlaceholder)
		} else {
			mediaTypes := configMediaTypesFor(artifactType)
			if mediaTypes == nil {
				// Unknown artifact_type — match nothing rather than silently
				// returning every repository.
				return nil, nil
			}
			args = append(args, mediaTypes)
			filterClause = fmt.Sprintf(`
			   AND EXISTS (
			       SELECT 1 FROM manifests m
			       WHERE m.repo_id = r.id
			         AND m.tenant_id = r.tenant_id
			         AND m.config_media_type = ANY($%d)
			   )`, nextPlaceholder)
		}
	}

	q := `SELECT ` + repoSelectCols + `
	     FROM repositories r
	     JOIN organizations o ON o.id = r.org_id
	     WHERE r.tenant_id = $1` + whereOrg + filterClause + `
	     ORDER BY r.name`

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()

	var repos []*metadatav1.Repository
	for rows.Next() {
		var repo metadatav1.Repository
		var createdAt time.Time
		// FE-API-006 added r.description to repoSelectCols; the scan needs to
		// drain all 10 columns or pgx returns "number of field descriptions
		// must equal number of destinations". Keep field order aligned with
		// repoSelectCols and scanOneRepo.
		if err := rows.Scan(&repo.RepoId, &repo.OrgId, &repo.TenantId, &repo.Name,
			&repo.IsPublic, &repo.StorageQuota, &repo.StorageUsed, &createdAt, &repo.Org,
			&repo.Description,
			&repo.ImmutableTags,
			&repo.RequireSignature); err != nil {
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
	// Uses the shared repoSelectCols so a new field added to that constant
	// flows here automatically (e.g. the immutable_tags column added in
	// the futures.md Tier 1 #2 work landed without touching this query).
	const q = `
		WITH updated AS (
			UPDATE repositories SET storage_quota = $1
			WHERE  id = $2 AND tenant_id = $3
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, created_at, description, immutable_tags, require_signature
		)
		SELECT ` + repoSelectCols + `
		FROM   updated r
		JOIN   organizations o ON o.id = r.org_id`
	return r.scanOneRepo(ctx, q, quota, repoID, tenantID)
}

// UpdateRepositorySignaturePolicy flips the repo-wide `require_signature`
// flag. Returns the updated Repository so the caller can echo state back
// without a follow-up GetRepository. Separate RPC from UpdateRepository
// for the same audit-trail-clarity reason as UpdateRepositoryImmutability.
//
// Futures.md Tier 1 #3 — Signed-image admission policy.
func (r *Repository) UpdateRepositorySignaturePolicy(ctx context.Context, tenantID, repoID string, requireSignature bool) (*metadatav1.Repository, error) {
	const q = `
		WITH updated AS (
			UPDATE repositories SET require_signature = $1
			WHERE  id = $2 AND tenant_id = $3
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, created_at, description, immutable_tags, require_signature
		)
		SELECT ` + repoSelectCols + `
		FROM   updated r
		JOIN   organizations o ON o.id = r.org_id`
	return r.scanOneRepo(ctx, q, requireSignature, repoID, tenantID)
}

// ListRepositoryTrustedKeys returns every approved signing key for a
// repo. services/core's admission gate calls this on every pull when
// `require_signature=true`; the dashboard calls it to render the
// allowlist table on the Settings tab. Ordered by `added_at` so the
// most recently approved key is at the bottom of the list — operators
// typically scan top-down for the original approval.
//
// Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist.
func (r *Repository) ListRepositoryTrustedKeys(ctx context.Context, tenantID, repoID string) ([]*metadatav1.RepositoryTrustedKey, error) {
	const q = `
		SELECT id, repo_id, tenant_id, key_id,
		       COALESCE(display_name, ''),
		       COALESCE(added_by::text, ''),
		       added_at
		FROM   repository_trusted_keys
		WHERE  tenant_id = $1 AND repo_id = $2
		ORDER  BY added_at ASC`
	rows, err := r.reader().Query(ctx, q, tenantID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list trusted keys: %w", err)
	}
	defer rows.Close()

	var keys []*metadatav1.RepositoryTrustedKey
	for rows.Next() {
		var k metadatav1.RepositoryTrustedKey
		var addedAt time.Time
		if err := rows.Scan(&k.Id, &k.RepoId, &k.TenantId, &k.KeyId,
			&k.DisplayName, &k.AddedBy, &addedAt); err != nil {
			return nil, fmt.Errorf("scan trusted key: %w", err)
		}
		k.AddedAt = timestamppb.New(addedAt)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// AddRepositoryTrustedKey idempotently approves a signing key for the
// repo. Re-adding an already-approved (repo_id, key_id) is a no-op
// that returns the existing row — display_name and added_by from the
// new request are silently dropped so the original approval stays the
// load-bearing audit anchor. `added_by` is permitted to be empty
// (system-driven add); when set it must be a UUID matching the user
// or service-account that authorised the change.
//
// Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist.
func (r *Repository) AddRepositoryTrustedKey(ctx context.Context, tenantID, repoID, keyID, displayName, addedBy string) (*metadatav1.RepositoryTrustedKey, error) {
	// addedBy is a string in the proto so the caller doesn't have to
	// parse UUIDs; we convert to *string here so empty maps to NULL
	// rather than the literal "" — keeps the column type-clean and
	// preserves the audit chain when the database is inspected raw.
	var addedByArg any
	if addedBy != "" {
		addedByArg = addedBy
	}
	const q = `
		INSERT INTO repository_trusted_keys (repo_id, tenant_id, key_id, display_name, added_by)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		ON CONFLICT (repo_id, key_id) DO UPDATE
		    SET key_id = EXCLUDED.key_id  -- no-op; just triggers RETURNING
		RETURNING id, repo_id, tenant_id, key_id,
		          COALESCE(display_name, ''),
		          COALESCE(added_by::text, ''),
		          added_at`
	var k metadatav1.RepositoryTrustedKey
	var addedAt time.Time
	if err := r.pool.QueryRow(ctx, q, repoID, tenantID, keyID, displayName, addedByArg).Scan(
		&k.Id, &k.RepoId, &k.TenantId, &k.KeyId,
		&k.DisplayName, &k.AddedBy, &addedAt,
	); err != nil {
		return nil, fmt.Errorf("add trusted key: %w", err)
	}
	k.AddedAt = timestamppb.New(addedAt)
	return &k, nil
}

// RemoveRepositoryTrustedKey deletes an approval. Removing the last
// key in the allowlist widens the gate back to "ANY signature passes"
// (Phase 1 fallback) by design — see migration 00016 header for the
// rationale. ErrNotFound when the (repo_id, key_id) pair doesn't
// exist so the caller can distinguish "already removed" from real
// failures.
//
// Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist.
func (r *Repository) RemoveRepositoryTrustedKey(ctx context.Context, tenantID, repoID, keyID string) error {
	const q = `DELETE FROM repository_trusted_keys
	            WHERE tenant_id = $1 AND repo_id = $2 AND key_id = $3`
	tag, err := r.pool.Exec(ctx, q, tenantID, repoID, keyID)
	if err != nil {
		return fmt.Errorf("delete trusted key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRepositoryImmutability flips the repo-wide `immutable_tags` flag.
// Returns the updated Repository so the caller can echo state back
// without a follow-up GetRepository. Separate RPC from UpdateRepository
// so the audit trail records the security-relevant change explicitly.
//
// Futures.md Tier 1 #2 — Tag immutability + image promotion workflow.
func (r *Repository) UpdateRepositoryImmutability(ctx context.Context, tenantID, repoID string, immutable bool) (*metadatav1.Repository, error) {
	const q = `
		WITH updated AS (
			UPDATE repositories SET immutable_tags = $1
			WHERE  id = $2 AND tenant_id = $3
			RETURNING id, org_id, tenant_id, name, is_public, storage_quota, created_at, description, immutable_tags, require_signature
		)
		SELECT ` + repoSelectCols + `
		FROM   updated r
		JOIN   organizations o ON o.id = r.org_id`
	return r.scanOneRepo(ctx, q, immutable, repoID, tenantID)
}

// UpdateTagImmutable flips the per-tag pin. Independent of the repo-wide
// `immutable_tags` flag — a pinned tag stays locked even when the parent
// repo is mutable. Returns the updated Tag so the caller can echo state
// back without a follow-up GetTag.
//
// Futures.md Tier 1 #2 — Tag immutability + image promotion workflow.
func (r *Repository) UpdateTagImmutable(ctx context.Context, tenantID, repoID, name string, immutable bool) (*metadatav1.Tag, error) {
	// CTE-then-join so the response includes the joined manifests row
	// fields (size, quarantine state, retention pending stamp) that
	// scanOneTag expects. RETURNING from the bare UPDATE can't reach
	// the manifests join.
	const q = `
		WITH updated AS (
			UPDATE tags
			   SET immutable = $1, updated_at = now()
			 WHERE repo_id = $2 AND tenant_id = $3 AND name = $4
		 RETURNING id, repo_id, tenant_id, name, manifest_digest, updated_at, created_at, immutable
		)
		SELECT t.id, t.repo_id, t.tenant_id, t.name, t.manifest_digest,
		       t.updated_at, t.created_at, COALESCE(m.image_size_bytes, 0),
		       m.retention_pending_delete_at,
		       COALESCE(m.quarantined, FALSE),
		       COALESCE(m.config_media_type, ''),
		       COALESCE(m.media_type, ''),
		       t.immutable
		FROM   updated t
		LEFT   JOIN manifests m
		  ON   m.repo_id   = t.repo_id
		  AND  m.tenant_id = t.tenant_id
		  AND  m.digest    = t.manifest_digest`
	return r.scanOneTag(ctx, q, immutable, repoID, tenantID, name)
}

func (r *Repository) scanOneRepo(ctx context.Context, query string, args ...any) (*metadatav1.Repository, error) {
	var repo metadatav1.Repository
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&repo.RepoId, &repo.OrgId, &repo.TenantId, &repo.Name,
		&repo.IsPublic, &repo.StorageQuota, &repo.StorageUsed, &createdAt, &repo.Org,
		&repo.Description,
		&repo.ImmutableTags,
		&repo.RequireSignature,
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

// LookupOrgIDByName returns the org_id for (tenant_id, name). Unlike
// GetOrCreateOrganization this is a pure read — NotFound when the org does
// not exist. Added in FE-API-039 so the management BFF can map org-name URLs
// (e.g. /api/v1/orgs/{org}/policies/retention) to the org_id required by
// the per-org retention RPCs without unintentionally creating the org.
func (r *Repository) LookupOrgIDByName(ctx context.Context, tenantID, orgName string) (string, error) {
	const q = `SELECT id::text FROM organizations WHERE tenant_id = $1 AND name = $2`
	var orgID string
	err := r.pool.QueryRow(ctx, q, tenantID, orgName).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lookup org by name: %w", err)
	}
	return orgID, nil
}

// ── Tags ────────────────────────────────────────────────────────────────────

// tagSelectCols is the column list for every Tag row read.
// LEFT JOIN against `manifests` keeps the row when the referenced manifest is
// missing (transient state during deletes) and returns size_bytes=0 in that
// case instead of dropping the tag from the result set.
//
// REM-013 gap 1: m.retention_pending_delete_at surfaces on the wire so the
// dashboard can render "🗑 deletes in N days" pills on the Tags table
// without an extra round-trip. NULL when the manifest isn't in the
// retention soft-delete window (the common case).
//
// FE-API-050: COALESCE(m.quarantined, FALSE) surfaces the parent
// manifest's quarantine flag on every tag row so the Tags table can
// render a 🔒 pill without per-row GetManifest calls. The LEFT JOIN
// also covers the transient "tag exists, manifest row missing" state
// during deletes — the coalesce ensures the column scans as false
// rather than NULL.
// S-MAINT-1 Batch 5: COALESCE config_media_type to ” so NULL legacy
// rows scan as empty string (artifact_type is then derived in Go and
// left empty so the FE renders the "Unknown" pill tone).
//
// REM-020 Fix A: also pull the manifest's own media_type so
// deriveArtifactType can classify OCI image indexes / Docker manifest
// lists as "image". These rows have a NULL config_media_type by design
// (an index is a pointer at per-arch manifests, not an image config)
// and were previously emitted with artifact_type="" — invisible to the
// repo Tags tab's `?type=image` filter (added to differentiate Helm vs
// image artifacts) even though they ARE images.
const tagSelectCols = `t.id, t.repo_id, t.tenant_id, t.name, t.manifest_digest,
	t.updated_at, t.created_at, COALESCE(m.image_size_bytes, 0),
	m.retention_pending_delete_at,
	COALESCE(m.quarantined, FALSE),
	COALESCE(m.config_media_type, ''),
	COALESCE(m.media_type, ''),
	t.immutable`

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
			RETURNING id, repo_id, tenant_id, name, manifest_digest, updated_at, created_at, immutable
		)
		SELECT t.id, t.repo_id, t.tenant_id, t.name, t.manifest_digest,
		       t.updated_at, t.created_at, COALESCE(m.image_size_bytes, 0),
		       m.retention_pending_delete_at,
		       COALESCE(m.quarantined, FALSE),
		       COALESCE(m.config_media_type, ''),
		       COALESCE(m.media_type, ''),
		       t.immutable
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
		// REM-013 gap 1: scan retention_pending_delete_at as *time.Time so
		// NULL (the common case) survives unmodified and we can leave the
		// proto field unset rather than emitting the Go zero time.
		var pendingDeleteAt *time.Time
		// S-MAINT-1 Batch 5: pull config_media_type so artifact_type can
		// be derived in Go (deriveArtifactType) without leaking the raw
		// mediaType taxonomy on the wire.
		// REM-020 Fix A: also pull mediaType so multi-arch image indexes
		// (no config_media_type) are still classified as "image".
		var configMediaType, mediaType string
		if err := rows.Scan(&tag.TagId, &tag.RepoId, &tag.TenantId, &tag.Name,
			&tag.ManifestDigest, &updatedAt, &createdAt, &tag.SizeBytes,
			&pendingDeleteAt, &tag.Quarantined, &configMediaType, &mediaType,
			&tag.Immutable); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tag.UpdatedAt = timestamppb.New(updatedAt)
		tag.CreatedAt = timestamppb.New(createdAt)
		if pendingDeleteAt != nil {
			tag.RetentionPendingDeleteAt = timestamppb.New(*pendingDeleteAt)
		}
		tag.ArtifactType = deriveArtifactType(configMediaType, mediaType)
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
	// REM-013 gap 1: see ListTags for the *time.Time scan rationale.
	var pendingDeleteAt *time.Time
	// S-MAINT-1 Batch 5: artifact_type derivation mirrors ListTags.
	// REM-020 Fix A: media_type also pulled so multi-arch image indexes
	// classify as "image".
	var configMediaType, mediaType string
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&tag.TagId, &tag.RepoId, &tag.TenantId, &tag.Name,
		&tag.ManifestDigest, &updatedAt, &createdAt, &tag.SizeBytes,
		&pendingDeleteAt, &tag.Quarantined, &configMediaType, &mediaType,
		&tag.Immutable,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	tag.ArtifactType = deriveArtifactType(configMediaType, mediaType)
	tag.UpdatedAt = timestamppb.New(updatedAt)
	tag.CreatedAt = timestamppb.New(createdAt)
	if pendingDeleteAt != nil {
		tag.RetentionPendingDeleteAt = timestamppb.New(*pendingDeleteAt)
	}
	return &tag, nil
}

// ── Manifests ───────────────────────────────────────────────────────────────

// FE-API-050 — manifestSelectCols is the column list every Manifest
// read SHARES, including the four quarantine columns introduced in
// migration 00012. Keeping it as a constant means a new reader can't
// accidentally drop them and lie about the quarantine state by
// omission.
// S-MAINT-1 Batch 5: config_media_type joined in via COALESCE so legacy
// rows that pre-date migration 00013 (or where backfill missed) still
// scan into an empty string rather than NULL. artifact_type is derived
// in Go (deriveArtifactType) not in SQL because the mapping is a one-
// line switch that prefers code-review visibility over hidden SQL CASE.
const manifestSelectCols = `id, repo_id, tenant_id, digest, media_type, raw_json,
	size_bytes, created_at,
	quarantined, COALESCE(quarantine_reason, ''),
	quarantined_at, COALESCE(quarantined_by, ''),
	COALESCE(config_media_type, '')`

// PutManifest upserts a manifest row.
//
// `size_bytes` is the on-the-wire size of the manifest document itself; the
// aggregate image size (config blob + sum of layer blob sizes, or for an
// index, the sum of child manifest sizes) is parsed from rawJSON via
// parseImageSize and stored in `image_size_bytes` so the tag-level size can
// be returned in O(1) without re-parsing on every read.
//
// REM-021: when the manifest is an OCI image index / Docker manifest list,
// the function additionally:
//
//   - parses `manifests[].digest` and inserts one row per child into
//     `manifest_children` (idempotent on (tenant_id, parent_digest,
//     child_digest)). This is the reachability link retention.eval uses
//     so child platform manifests aren't classified as dangling.
//   - recomputes image_size_bytes as the SUM of children's
//     `manifests.image_size_bytes` from the DB, replacing the buggy
//     parseImageSize fallback that summed child manifest *doc* sizes
//     (~400 B each) instead of layer totals. Docker pushes children
//     before the index so the children are normally present at this
//     point; when a client pushes the index first the function gracefully
//     stores 0 until a re-push lands.
//
// Single-arch images (no index → no child digests parsed) take the same
// path as before — zero behavioural change.
func (r *Repository) PutManifest(ctx context.Context, tenantID, repoID, digest, mediaType string, rawJSON []byte, sizeBytes int64) (*metadatav1.Manifest, error) {
	// S-MAINT-1 Batch 5: parse config.mediaType once at push time so the
	// indexed column stays in sync with raw_json without a follow-up
	// scan. On re-push of the same digest the EXCLUDED.config_media_type
	// path keeps the column accurate even if a manifest's contents are
	// rewritten (uncommon but legal under OCI).
	configMediaType := parseConfigMediaType(rawJSON)
	childDigests := parseChildManifestDigests(rawJSON, mediaType)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin put-manifest tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const upsertQ = `
		INSERT INTO manifests (repo_id, tenant_id, digest, media_type, raw_json, size_bytes, image_size_bytes, config_media_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
		ON CONFLICT (repo_id, digest) DO UPDATE
		  SET media_type        = EXCLUDED.media_type,
		      raw_json          = EXCLUDED.raw_json,
		      size_bytes        = EXCLUDED.size_bytes,
		      image_size_bytes  = EXCLUDED.image_size_bytes,
		      config_media_type = EXCLUDED.config_media_type`
	if _, err := tx.Exec(ctx, upsertQ,
		repoID, tenantID, digest, mediaType, rawJSON, sizeBytes,
		parseImageSize(rawJSON), configMediaType,
	); err != nil {
		return nil, fmt.Errorf("upsert manifest: %w", err)
	}

	if len(childDigests) > 0 {
		// REM-021: persist parent→child link. Idempotent on (tenant_id,
		// parent_digest, child_digest) so re-pushing the same index is
		// a no-op for the reachability graph.
		const childUpsertQ = `
			INSERT INTO manifest_children (repo_id, tenant_id, parent_digest, child_digest)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, parent_digest, child_digest) DO NOTHING`
		for _, c := range childDigests {
			if _, err := tx.Exec(ctx, childUpsertQ, repoID, tenantID, digest, c); err != nil {
				return nil, fmt.Errorf("upsert manifest_children: %w", err)
			}
		}
		// REM-021: replace the parseImageSize undercount with a real sum
		// of children's image_size_bytes from the DB. Children that
		// haven't landed yet (rare — docker push order is children
		// first) contribute 0; a later re-push of the index will pick
		// them up.
		const recalcSizeQ = `
			UPDATE manifests
			SET    image_size_bytes = COALESCE(
			           (SELECT SUM(image_size_bytes)
			              FROM manifests
			             WHERE tenant_id = $1
			               AND digest    = ANY($2)),
			           0)
			WHERE  tenant_id = $1
			  AND  repo_id   = $3
			  AND  digest    = $4`
		if _, err := tx.Exec(ctx, recalcSizeQ, tenantID, childDigests, repoID, digest); err != nil {
			return nil, fmt.Errorf("recalc parent image_size_bytes: %w", err)
		}
	}

	const selectQ = `SELECT ` + manifestSelectCols + `
		FROM manifests
		WHERE repo_id = $1 AND digest = $2 AND tenant_id = $3`
	var m metadatav1.Manifest
	var createdAt time.Time
	var qReason, qBy string
	var qAt *time.Time
	var cmt string
	if err := tx.QueryRow(ctx, selectQ, repoID, digest, tenantID).Scan(
		&m.ManifestId, &m.RepoId, &m.TenantId, &m.Digest,
		&m.MediaType, &m.RawJson, &m.SizeBytes, &createdAt,
		&m.Quarantined, &qReason, &qAt, &qBy, &cmt,
	); err != nil {
		return nil, fmt.Errorf("select put manifest: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit put-manifest tx: %w", err)
	}
	m.CreatedAt = timestamppb.New(createdAt)
	m.QuarantineReason = qReason
	m.QuarantinedBy = qBy
	if qAt != nil {
		m.QuarantinedAt = timestamppb.New(*qAt)
	}
	m.ConfigMediaType = cmt
	m.ArtifactType = deriveArtifactType(cmt, m.MediaType)
	return &m, nil
}

// parseChildManifestDigests pulls the per-platform child digests out of
// an OCI image index / Docker manifest list. Returns an empty slice for
// any other media type so callers can skip the child-row upsert without
// branching on mediaType themselves.
//
// REM-021. Bounded by maxManifestEntries so a crafted index can't drive
// an unbounded loop / unbounded INSERT. Digest values are NOT validated
// against the sha256 regex here — callers persist the strings verbatim
// and downstream queries match on equality, so a malformed digest
// simply won't join to any existing manifest row (the desired graceful
// behaviour).
func parseChildManifestDigests(rawJSON []byte, mediaType string) []string {
	switch mediaType {
	case "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		// proceed
	default:
		return nil
	}
	if len(rawJSON) == 0 {
		return nil
	}
	var doc struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		return nil
	}
	entries := doc.Manifests
	if len(entries) > maxManifestEntries {
		entries = entries[:maxManifestEntries]
	}
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.Digest == "" {
			continue
		}
		if _, ok := seen[e.Digest]; ok {
			continue
		}
		seen[e.Digest] = struct{}{}
		out = append(out, e.Digest)
	}
	return out
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

// parseConfigMediaType extracts `config.mediaType` from an OCI manifest
// document. Used at PutManifest time to populate the indexed
// `config_media_type` column without re-parsing the manifest on every
// read. Returns the empty string when the JSON is malformed, has no
// config block, or the mediaType key is missing — callers store "" and
// any artifact_type derivation downstream treats that as unknown.
//
// S-MAINT-1 Batch 5 (P6 + F4): introduces the discriminator that lets
// the scanner skip non-image artifacts and the dashboard render the
// per-tag artifact-type pill.
func parseConfigMediaType(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		return ""
	}
	var doc struct {
		Config *struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
	}
	if err := json.Unmarshal(rawJSON, &doc); err != nil {
		return ""
	}
	if doc.Config == nil {
		return ""
	}
	return doc.Config.MediaType
}

// deriveArtifactType maps the manifest's `config.mediaType` (preferred)
// or — when that's empty — its top-level `mediaType` to the stable
// discriminator used on the wire (proto field `artifact_type`). Keeping
// the mapping in one place means a new OCI artifact category only needs
// an entry here — no schema change, no proto change.
//
// REM-020 Fix A: OCI image indexes + Docker manifest lists (multi-arch
// images) carry NULL config_media_type — an index is a pointer at
// per-arch manifests, not an image config. Before this fix they got
// artifact_type="" and were hidden by the repo Tags tab's
// `?type=image` filter (added to differentiate Helm vs image artifacts).
// Now they classify as "image" via the mediaType fallback so the filter
// includes them.
//
// Returns "" only when BOTH inputs are empty (NULL in DB) so callers can
// still tell "unknown legacy row" apart from "recognised manifest,
// unknown artifact category" ("other").
func deriveArtifactType(configMediaType, mediaType string) string {
	switch configMediaType {
	case "application/vnd.docker.container.image.v1+json",
		"application/vnd.oci.image.config.v1+json":
		return "image"
	case "application/vnd.cncf.helm.config.v1+json":
		return "helm"
	case "application/vnd.dev.cosign.simplesigning.v1+json",
		"application/vnd.dsse.envelope.v1+json":
		return "signature"
	case "application/spdx+json",
		"application/vnd.cyclonedx+json":
		return "sbom"
	case "":
		// Fall through to the mediaType-based classification below.
	default:
		return "other"
	}
	// configMediaType was empty — try the manifest-level mediaType. This
	// catches manifest indexes (multi-arch images) which legitimately
	// have no config_media_type.
	switch mediaType {
	case "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		return "image"
	}
	return ""
}

// configMediaTypesFor is the reverse of deriveArtifactType — given a
// stable artifact_type discriminator, return the set of `config.mediaType`
// strings that belong to that bucket. Used by ListRepositories to filter
// the repos table to those that hold at least one manifest of the
// requested kind via a JOIN-EXISTS on the manifests table.
//
// The "other" bucket can't be enumerated (it's the deriveArtifactType
// fall-through) so callers asking for "other" must NOT IN every known
// media type rather than IN a fixed list. ListRepositories handles that
// special case inline.
//
// Returns nil for unknown artifact_type values so callers can treat them
// as "no rows match" without surprising NULL semantics.
func configMediaTypesFor(artifactType string) []string {
	switch artifactType {
	case "image":
		return []string{
			"application/vnd.docker.container.image.v1+json",
			"application/vnd.oci.image.config.v1+json",
		}
	case "helm":
		return []string{
			"application/vnd.cncf.helm.config.v1+json",
		}
	case "signature":
		return []string{
			"application/vnd.dev.cosign.simplesigning.v1+json",
			"application/vnd.dsse.envelope.v1+json",
		}
	case "sbom":
		return []string{
			"application/spdx+json",
			"application/vnd.cyclonedx+json",
		}
	default:
		return nil
	}
}

// allKnownConfigMediaTypes is the union of every value configMediaTypesFor
// returns — used by the "other" filter path to project everything that
// ISN'T a known artifact type. Kept derived (not a separate const) so a
// new entry in configMediaTypesFor automatically updates the negation.
func allKnownConfigMediaTypes() []string {
	out := []string{}
	for _, t := range []string{"image", "helm", "signature", "sbom"} {
		out = append(out, configMediaTypesFor(t)...)
	}
	return out
}

func parseImageSize(rawJSON []byte) int64 {
	if len(rawJSON) == 0 {
		return 0
	}
	var doc struct {
		Config *struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
		Manifests []struct {
			Size int64 `json:"size"`
		} `json:"manifests"`
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
		SELECT ` + manifestSelectCols + `
		FROM   manifests
		WHERE  repo_id = $1 AND digest = $2 AND tenant_id = $3`
	return r.scanOneManifest(ctx, q, repoID, digest, tenantID)
}

// UpdateManifestQuarantine flips the quarantine state on a manifest.
// Idempotent on repeated true→true transitions — quarantined_at +
// quarantined_by are preserved on re-application so the audit trail
// keeps the FIRST event's timestamp. quarantined=false clears all four
// columns atomically.
//
// Returns the updated manifest row so the caller (scanner / management
// BFF) can echo state back without an extra GetManifest round-trip.
// ErrNotFound when no row matches (tenant_id is in the WHERE clause,
// so cross-tenant attempts surface as NotFound without leaking state).
func (r *Repository) UpdateManifestQuarantine(
	ctx context.Context,
	tenantID, repoID, digest string,
	quarantined bool,
	reason, by string,
) (*metadatav1.Manifest, error) {
	if quarantined {
		// quarantined_at + quarantined_by are COALESCED so an already-
		// quarantined manifest keeps its original stamps. The reason
		// IS updated on re-application so a follow-up scan that finds
		// more severe findings can refresh the operator-readable
		// message without losing the original timestamp.
		const q = `
			UPDATE manifests
			   SET quarantined        = TRUE,
			       quarantine_reason  = $4,
			       quarantined_at     = COALESCE(quarantined_at, NOW()),
			       quarantined_by     = COALESCE(quarantined_by, $5)
			 WHERE repo_id = $1 AND digest = $2 AND tenant_id = $3
			 RETURNING ` + manifestSelectCols
		return r.scanOneManifest(ctx, q, repoID, digest, tenantID, reason, by)
	}
	// Clear path — null out all four columns in one update.
	const q = `
		UPDATE manifests
		   SET quarantined        = FALSE,
		       quarantine_reason  = NULL,
		       quarantined_at     = NULL,
		       quarantined_by     = NULL
		 WHERE repo_id = $1 AND digest = $2 AND tenant_id = $3
		 RETURNING ` + manifestSelectCols
	return r.scanOneManifest(ctx, q, repoID, digest, tenantID)
}

// ListTagNamesByDigest returns the names of every tag in a repo that
// currently points at the given manifest digest. Used by the
// UpdateManifestQuarantine handler to bust the GetManifest Redis cache
// entries that resolved by tag — without this, a quarantine flip would
// take up to the cache TTL (5min) to reflect at the pull-time gate.
//
// Cheap query — manifests rarely have more than a handful of tags
// pointing at them. tenant_id is in the predicate so cross-tenant
// digest collisions stay isolated.
func (r *Repository) ListTagNamesByDigest(ctx context.Context, tenantID, repoID, digest string) ([]string, error) {
	const q = `SELECT name FROM tags
	           WHERE  repo_id = $1 AND tenant_id = $2 AND manifest_digest = $3`
	rows, err := r.pool.Query(ctx, q, repoID, tenantID, digest)
	if err != nil {
		return nil, fmt.Errorf("ListTagNamesByDigest: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan tag name: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
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
		SELECT m.id, m.repo_id, m.tenant_id, m.digest, m.media_type, m.raw_json,
		       m.size_bytes, m.created_at,
		       m.quarantined, COALESCE(m.quarantine_reason, ''),
		       m.quarantined_at, COALESCE(m.quarantined_by, '')
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
		var qReason, qBy string
		var qAt *time.Time
		// S-MAINT-1 Batch 5: same shape as scanOneManifest — derive
		// artifact_type from config_media_type in Go after scan.
		var configMediaType string
		if err := rows.Scan(&m.ManifestId, &m.RepoId, &m.TenantId, &m.Digest,
			&m.MediaType, &m.RawJson, &m.SizeBytes, &createdAt,
			&m.Quarantined, &qReason, &qAt, &qBy,
			&configMediaType); err != nil {
			return nil, fmt.Errorf("scan manifest: %w", err)
		}
		m.CreatedAt = timestamppb.New(createdAt)
		m.QuarantineReason = qReason
		m.QuarantinedBy = qBy
		if qAt != nil {
			m.QuarantinedAt = timestamppb.New(*qAt)
		}
		m.ConfigMediaType = configMediaType
		// REM-020 Fix A: pass MediaType so multi-arch image indexes
		// classify as "image" via the manifest-level fallback.
		m.ArtifactType = deriveArtifactType(configMediaType, m.MediaType)
		manifests = append(manifests, &m)
	}
	return manifests, rows.Err()
}

func (r *Repository) scanOneManifest(ctx context.Context, query string, args ...any) (*metadatav1.Manifest, error) {
	var m metadatav1.Manifest
	var createdAt time.Time
	var qReason, qBy string
	var qAt *time.Time
	// S-MAINT-1 Batch 5: config_media_type scans into a local string;
	// artifact_type derived in Go from that mediaType (see
	// deriveArtifactType). Keeping the derivation outside SQL keeps the
	// mapping reviewable as plain Go code.
	var configMediaType string
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&m.ManifestId, &m.RepoId, &m.TenantId, &m.Digest,
		&m.MediaType, &m.RawJson, &m.SizeBytes, &createdAt,
		&m.Quarantined, &qReason, &qAt, &qBy,
		&configMediaType,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.CreatedAt = timestamppb.New(createdAt)
	m.QuarantineReason = qReason
	m.QuarantinedBy = qBy
	if qAt != nil {
		m.QuarantinedAt = timestamppb.New(*qAt)
	}
	m.ConfigMediaType = configMediaType
	// REM-020 Fix A: see ListManifests for the manifest-level mediaType fallback.
	m.ArtifactType = deriveArtifactType(configMediaType, m.MediaType)
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
	// S-MAINT-1 B3: used_bytes sums image_size_bytes on manifests directly
	// rather than the unmaintained `repositories.storage_used` column.
	const q = `
		SELECT
		    COALESCE((SELECT SUM(image_size_bytes) FROM manifests WHERE tenant_id = $1), 0) AS used_bytes,
		    t.storage_quota                                                                  AS quota_bytes
		FROM   tenants t
		WHERE  t.id = $1`

	var usage metadatav1.QuotaUsage
	usage.TenantId = tenantID
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&usage.UsedBytes, &usage.QuotaBytes); err != nil {
		return nil, fmt.Errorf("get quota usage: %w", err)
	}
	return &usage, nil
}

// GetTenantStorageBreakdown returns the tenant's total storage usage and the
// top-50 repositories by storage_used. Backs FE-API-031 /api/v1/stats/storage.
//
// One query, one round-trip: a CTE computes the tenant sum, then the outer
// query orders the per-repo rows by storage_used DESC and joins the tenant
// total so percent_of_tenant is materialised server-side (so every UI surface
// renders identical numbers and the math stays in one place).
//
// Capped at 50 rows. The top-N is itself the answer; pagination is intentionally
// not exposed for v1.
func (r *Repository) GetTenantStorageBreakdown(ctx context.Context, tenantID string) (*metadatav1.GetTenantStorageBreakdownResponse, error) {
	// S-MAINT-1 B3: per-repo bytes + tenant total both compute from
	// manifests.image_size_bytes. The `repo_sizes` CTE groups by repo_id
	// once so the outer SELECT can LEFT JOIN against it (repos with zero
	// manifests still appear via the LEFT JOIN with rs.bytes = NULL → 0).
	const q = `
		WITH total AS (
		    SELECT COALESCE(SUM(image_size_bytes), 0)::BIGINT AS bytes
		    FROM   manifests
		    WHERE  tenant_id = $1
		),
		repo_sizes AS (
		    SELECT repo_id, SUM(image_size_bytes)::BIGINT AS bytes
		    FROM   manifests
		    WHERE  tenant_id = $1
		    GROUP  BY repo_id
		)
		SELECT r.id::text,
		       o.name                                                                AS org,
		       r.name                                                                AS name,
		       COALESCE(rs.bytes, 0)::BIGINT                                         AS storage_used,
		       CASE WHEN total.bytes = 0 THEN 0.0
		            ELSE 100.0 * (COALESCE(rs.bytes, 0)::DOUBLE PRECISION / total.bytes::DOUBLE PRECISION)
		       END                                                                   AS percent_of_tenant,
		       total.bytes                                                           AS tenant_total
		FROM   repositories r
		JOIN   organizations o ON o.id = r.org_id
		LEFT   JOIN repo_sizes rs ON rs.repo_id = r.id
		CROSS  JOIN total
		WHERE  r.tenant_id = $1
		ORDER  BY COALESCE(rs.bytes, 0) DESC, r.name ASC
		LIMIT  50`

	rows, err := r.reader().Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("storage breakdown: %w", err)
	}
	defer rows.Close()

	resp := &metadatav1.GetTenantStorageBreakdownResponse{
		Repositories: make([]*metadatav1.RepositoryStorageEntry, 0, 50),
	}
	for rows.Next() {
		entry := &metadatav1.RepositoryStorageEntry{}
		var tenantTotal int64
		if err := rows.Scan(
			&entry.RepoId,
			&entry.Org,
			&entry.Name,
			&entry.StorageUsedBytes,
			&entry.PercentOfTenant,
			&tenantTotal,
		); err != nil {
			return nil, fmt.Errorf("storage breakdown scan: %w", err)
		}
		// tenant_total is identical across all rows; capture it once.
		resp.TenantStorageUsedBytes = tenantTotal
		resp.Repositories = append(resp.Repositories, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage breakdown rows: %w", err)
	}

	// Zero-repo tenant: the CTE still resolves total=0; we need a separate
	// guard for the response struct since no rows means no tenantTotal was
	// captured. Re-query the total in that edge case. Cheap: one COALESCE
	// SELECT against an indexed tenant_id.
	if len(resp.Repositories) == 0 {
		// S-MAINT-1 B3: same source switch as the main query — manifests, not the
		// stale storage_used column.
		const zeroQuery = `SELECT COALESCE(SUM(image_size_bytes), 0)::BIGINT FROM manifests WHERE tenant_id = $1`
		if err := r.reader().QueryRow(ctx, zeroQuery, tenantID).Scan(&resp.TenantStorageUsedBytes); err != nil {
			return nil, fmt.Errorf("storage breakdown total: %w", err)
		}
	}
	return resp, nil
}

// GetTenantUsage returns the storage + count aggregates that back the
// admin tenant-detail endpoint (FE-API-028). One CTE produces every value so
// the management layer does not fan out across three RPCs for one page.
//
// Tenants whose tenants row does not yet exist in metadata (lazy creation via
// UpdateTenantQuota's UPSERT) report all-zero counts and zero quota — never
// an error — so the BFF can stitch a sensible "fresh tenant" response for
// tenants created in registry-tenant that have not yet seen push activity.
func (r *Repository) GetTenantUsage(ctx context.Context, tenantID string) (*metadatav1.TenantUsage, error) {
	// LEFT JOIN against tenants is intentional: we want NULL → 0 for newly
	// created tenants that haven't been registered in the metadata DB yet.
	// COALESCE returns 0 in the absence of repository rows too. SUM/COUNT
	// against an empty filter yield NULL by default, so wrap both.
	// S-MAINT-1 B3: used_bytes sums manifests directly. repo_count still
	// comes from `repositories` (one row per repo regardless of manifests).
	const q = `
		WITH usage AS (
		    SELECT
		        (SELECT COALESCE(SUM(image_size_bytes), 0)::BIGINT FROM manifests WHERE tenant_id = $1) AS used_bytes,
		        (SELECT COUNT(*)::BIGINT FROM repositories WHERE tenant_id = $1)                        AS repo_count
		),
		orgs AS (
		    SELECT COUNT(*)::BIGINT AS org_count
		    FROM organizations
		    WHERE tenant_id = $1
		),
		tenant AS (
		    SELECT storage_quota
		    FROM tenants
		    WHERE id = $1
		)
		SELECT usage.used_bytes,
		       COALESCE(tenant.storage_quota, 0)::BIGINT,
		       usage.repo_count,
		       orgs.org_count
		FROM usage
		CROSS JOIN orgs
		LEFT JOIN tenant ON true`

	var u metadatav1.TenantUsage
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&u.StorageUsedBytes,
		&u.StorageQuotaBytes,
		&u.RepositoryCount,
		&u.OrganizationCount,
	); err != nil {
		return nil, fmt.Errorf("get tenant usage: %w", err)
	}
	return &u, nil
}

// UpdateTenantQuota sets the tenant-level storage_quota. Used by the management
// API's super-admin quota route to bump quotas for large customers.
//
// UPSERT semantics: a tenant created via registry-tenant does not get a row
// in registry-metadata's tenants table until first push activity. Platform
// admins legitimately need to set quotas ahead of that first push — without
// the upsert this returned NotFound for every newly-created tenant. The
// placeholder name is the tenant_id stringified (the name column has a
// UNIQUE constraint but no semantic meaning to this service); the
// metadata-side onboarding flow can overwrite the placeholder later.
func (r *Repository) UpdateTenantQuota(ctx context.Context, tenantID string, quotaBytes int64) (*metadatav1.QuotaUsage, error) {
	// Pass tenantID as both $1 (UUID) and $2 (TEXT) — Postgres refuses to
	// deduce a single type for a placeholder used in two columns of
	// different types (SQLSTATE 42P08), so separate placeholders are
	// required even though they bind to the same value.
	const q = `
		INSERT INTO tenants (id, name, storage_quota)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET storage_quota = EXCLUDED.storage_quota`
	if _, err := r.pool.Exec(ctx, q, tenantID, tenantID, quotaBytes); err != nil {
		return nil, fmt.Errorf("upsert tenant quota: %w", err)
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

// UpsertScanResult inserts or updates a scan_results row keyed on scan_id.
//
// The scanner enqueues a job, runs the plugin, and writes the result
// back via this method — there is no separate Create step. The first
// call for a given scan_id INSERTs (using repo_id, manifest_digest,
// scanner_name, scanner_version to satisfy NOT NULL columns); a
// subsequent call for the same scan_id UPDATEs the mutable columns
// only. Identity columns (repo_id, manifest_digest, scanner_name,
// scanner_version) are not overwritten by COALESCE so a re-emit can't
// accidentally clobber them with empty values.
//
// Returns ErrNotFound only on a follow-up update where the scan_id
// already exists in another tenant — that should be unreachable in
// practice (scan_id is a fresh UUID per job) and signals a real bug.
func (r *Repository) UpsertScanResult(ctx context.Context, scanID, tenantID, status string, findingsJSON []byte, severityCounts map[string]int32, repoID, manifestDigest, scannerName, scannerVersion string) error {
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

	// On INSERT we need every NOT NULL column. On UPDATE we keep the
	// existing identity values (repo_id et al.) so a follow-up call
	// with empty strings does no harm; only the mutable columns change.
	const q = `
		INSERT INTO scan_results (
		    id, tenant_id, repo_id, manifest_digest,
		    scanner_name, scanner_version,
		    status, findings, severity_counts,
		    started_at, completed_at
		) VALUES (
		    $1, $2, $3, $4,
		    $5, $6,
		    $7, $8, $9,
		    $10, $11
		)
		ON CONFLICT (id) DO UPDATE
		SET status          = EXCLUDED.status,
		    findings        = EXCLUDED.findings,
		    severity_counts = EXCLUDED.severity_counts,
		    started_at      = COALESCE(scan_results.started_at, EXCLUDED.started_at),
		    completed_at    = EXCLUDED.completed_at,
		    scanner_name    = CASE WHEN EXCLUDED.scanner_name    <> '' THEN EXCLUDED.scanner_name    ELSE scan_results.scanner_name    END,
		    scanner_version = CASE WHEN EXCLUDED.scanner_version <> '' THEN EXCLUDED.scanner_version ELSE scan_results.scanner_version END
		WHERE scan_results.tenant_id = EXCLUDED.tenant_id`

	tag, err := r.pool.Exec(ctx, q,
		scanID, tenantID, repoID, manifestDigest,
		scannerName, scannerVersion,
		status, findingsJSON, countsJSON,
		startedAt, completedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert scan result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// ON CONFLICT WHERE clause filtered the update — only possible
		// if the scan_id is owned by a different tenant. Treat as a
		// tenant-isolation breach and surface NotFound (don't leak the
		// existence of the row to the caller).
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

// ── SBOM (FE-API-033) ────────────────────────────────────────────────────────

// SBOMResult is the value type returned by GetScanSBOM. Kept in the repository
// layer so the handler maps directly to the proto message without dragging
// repository types into protos.
type SBOMResult struct {
	Format   string // "spdx-json" | "cyclonedx-json"
	SBOMJSON []byte
}

// UpsertScanSBOM persists the SBOM bytes + format on the latest scan_results
// row for (tenant_id, manifest_digest). Returns ErrNotFound when no scan row
// exists yet — the scanner is expected to create the row via
// CreatePendingScanResult / UpsertScanResult before calling this.
//
// "Latest" matches GetScanResult's selection (ORDER BY created_at DESC LIMIT 1)
// so writers and readers always agree on which row carries the SBOM.
func (r *Repository) UpsertScanSBOM(ctx context.Context, tenantID, manifestDigest, format string, sbomJSON []byte) error {
	// Two-step: pick the most recent row, then update it. A single UPDATE … FROM
	// would work too, but the SELECT keeps the row identification identical to
	// GetScanResult's contract.
	const selectQ = `
		SELECT id
		FROM   scan_results
		WHERE  tenant_id = $1 AND manifest_digest = $2
		ORDER  BY created_at DESC
		LIMIT  1`

	var rowID string
	if err := r.pool.QueryRow(ctx, selectQ, tenantID, manifestDigest).Scan(&rowID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("select scan_results for sbom upsert: %w", err)
	}

	const updateQ = `
		UPDATE scan_results
		SET    sbom_format = $1,
		       sbom_json   = $2
		WHERE  id = $3 AND tenant_id = $4`

	tag, err := r.pool.Exec(ctx, updateQ, format, sbomJSON, rowID, tenantID)
	if err != nil {
		return fmt.Errorf("update scan_results sbom: %w", err)
	}
	// The row was just selected; a 0-RowsAffected here means a concurrent
	// delete or a tenant_id mismatch, both of which fail closed as NotFound.
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetScanSBOM returns the SBOM bytes + format for the latest scan of
// (tenant_id, manifest_digest). Returns ErrNotFound when no scan row exists
// OR when the row exists but has no SBOM persisted yet (sbom_json IS NULL) —
// the caller cannot distinguish these cases, but the management BFF only
// needs the "no SBOM available" branch to render the same 404 either way.
func (r *Repository) GetScanSBOM(ctx context.Context, tenantID, manifestDigest string) (*SBOMResult, error) {
	const q = `
		SELECT sbom_format, sbom_json
		FROM   scan_results
		WHERE  tenant_id = $1 AND manifest_digest = $2
		ORDER  BY created_at DESC
		LIMIT  1`

	var format *string
	var sbom []byte
	err := r.pool.QueryRow(ctx, q, tenantID, manifestDigest).Scan(&format, &sbom)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get scan sbom: %w", err)
	}
	// NULL columns ⇒ row exists but SBOM not yet generated. Surface as
	// NotFound so the BFF renders the same "no SBOM recorded" message for
	// "never scanned" and "scanned but no SBOM" alike.
	if format == nil || len(sbom) == 0 {
		return nil, ErrNotFound
	}
	return &SBOMResult{Format: *format, SBOMJSON: sbom}, nil
}

// CountRepositories returns the number of repositories owned by a tenant.
func (r *Repository) CountRepositories(ctx context.Context, tenantID string) (int64, error) {
	const q = `SELECT COUNT(*) FROM repositories WHERE tenant_id = $1`
	var n int64
	if err := r.pool.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("CountRepositories: %w", err)
	}
	return n, nil
}

// SecurityOverview is the value type returned by GetSecurityOverview. Kept in
// the repository layer so the handler can map it 1:1 to the proto message
// without re-importing repository types into protos.
type SecurityOverview struct {
	OpenVulnerabilitiesTotal int64
	Critical                 int32
	High                     int32
	Medium                   int32
	Low                      int32
	Negligible               int32
	TagsTotal                int64
	TagsScanned              int64
	RecentScans24h           int64
	DaysSinceLastScan        int64
}

// GetTenantSeverityBreakdown returns the per-severity vulnerability counts for
// a tenant, computed from the LATEST complete scan per (repo_id, manifest_digest).
// This deduplicates re-scanned tags so a re-scan does not double the dashboard
// numbers (FE-API-016).
//
// The query is a single round-trip CTE: `latest` picks the most recent
// scan_results row per tag using DISTINCT ON; the outer SELECT sums the JSONB
// severity counter keys. NULL/missing keys are coalesced to 0. Severity keys
// are upper-case to match the scanner plugins (CLAUDE.md §4.7).
func (r *Repository) GetTenantSeverityBreakdown(ctx context.Context, tenantID string) (critical, high, medium, low, negligible int32, err error) {
	const q = `
		WITH latest AS (
			SELECT DISTINCT ON (repo_id, manifest_digest) severity_counts
			FROM   scan_results
			WHERE  tenant_id = $1 AND status = 'complete'
			ORDER  BY repo_id, manifest_digest, completed_at DESC NULLS LAST, created_at DESC
		)
		SELECT
		  COALESCE(SUM((severity_counts->>'CRITICAL')::int),   0),
		  COALESCE(SUM((severity_counts->>'HIGH')::int),       0),
		  COALESCE(SUM((severity_counts->>'MEDIUM')::int),     0),
		  COALESCE(SUM((severity_counts->>'LOW')::int),        0),
		  COALESCE(SUM((severity_counts->>'NEGLIGIBLE')::int), 0)
		FROM latest`

	if err = r.reader().QueryRow(ctx, q, tenantID).Scan(&critical, &high, &medium, &low, &negligible); err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("GetTenantSeverityBreakdown: %w", err)
	}
	return critical, high, medium, low, negligible, nil
}

// GetSecurityOverview computes the tenant-scoped security summary backing
// FE-API-020. Implemented as a single CTE so we never load tag/scan rows
// into Go memory:
//
//   - `latest` picks the most recent scan_results row per (repo_id, manifest_digest)
//     so re-scanned tags only count once toward the severity sums.
//   - `tag_counts` returns (tags_total, tags_scanned), where tags_scanned is the
//     number of tags whose current manifest_digest has at least one scan_result row.
//   - `recency` returns the count of complete scans in the last 24h and the age in
//     whole days of the most recent complete scan (0 when never scanned).
//
// All three sub-queries are tenant-scoped on every row to honour the §9
// tenant-isolation rule.
func (r *Repository) GetSecurityOverview(ctx context.Context, tenantID string) (*SecurityOverview, error) {
	const q = `
		WITH latest AS (
			SELECT DISTINCT ON (repo_id, manifest_digest) severity_counts
			FROM   scan_results
			WHERE  tenant_id = $1 AND status = 'complete'
			ORDER  BY repo_id, manifest_digest, completed_at DESC NULLS LAST, created_at DESC
		),
		severities AS (
			SELECT
			  COALESCE(SUM((severity_counts->>'CRITICAL')::int),   0)::int AS critical,
			  COALESCE(SUM((severity_counts->>'HIGH')::int),       0)::int AS high,
			  COALESCE(SUM((severity_counts->>'MEDIUM')::int),     0)::int AS medium,
			  COALESCE(SUM((severity_counts->>'LOW')::int),        0)::int AS low,
			  COALESCE(SUM((severity_counts->>'NEGLIGIBLE')::int), 0)::int AS negligible
			FROM latest
		),
		tag_counts AS (
			SELECT
			  (SELECT COUNT(*) FROM tags WHERE tenant_id = $1)::bigint AS tags_total,
			  (SELECT COUNT(DISTINCT (t.repo_id, t.manifest_digest))
			     FROM tags t
			     WHERE t.tenant_id = $1
			       AND EXISTS (
			         SELECT 1 FROM scan_results sr
			         WHERE  sr.tenant_id = $1
			           AND  sr.repo_id = t.repo_id
			           AND  sr.manifest_digest = t.manifest_digest
			       ))::bigint AS tags_scanned
		),
		recency AS (
			SELECT
			  (SELECT COUNT(*) FROM scan_results
			     WHERE tenant_id = $1
			       AND status = 'complete'
			       AND completed_at >= now() - INTERVAL '24 hours')::bigint AS recent_scans_24h,
			  COALESCE(
			    (SELECT EXTRACT(EPOCH FROM (now() - MAX(completed_at)))::bigint / 86400
			       FROM scan_results
			       WHERE tenant_id = $1 AND status = 'complete' AND completed_at IS NOT NULL),
			    0
			  )::bigint AS days_since_last_scan
		)
		SELECT
		  s.critical, s.high, s.medium, s.low, s.negligible,
		  tc.tags_total, tc.tags_scanned,
		  rc.recent_scans_24h, rc.days_since_last_scan
		FROM   severities s, tag_counts tc, recency rc`

	var ov SecurityOverview
	if err := r.reader().QueryRow(ctx, q, tenantID).Scan(
		&ov.Critical, &ov.High, &ov.Medium, &ov.Low, &ov.Negligible,
		&ov.TagsTotal, &ov.TagsScanned,
		&ov.RecentScans24h, &ov.DaysSinceLastScan,
	); err != nil {
		return nil, fmt.Errorf("GetSecurityOverview: %w", err)
	}
	ov.OpenVulnerabilitiesTotal = int64(ov.Critical) + int64(ov.High) + int64(ov.Medium) + int64(ov.Low) + int64(ov.Negligible)
	return &ov, nil
}

// GetTenantVulnerabilityCount aggregates severity counts across a tenant's
// scanned manifests, deduplicated to the most recent complete scan per
// (repo_id, manifest_digest).
//
// S-MAINT-1 B1 fix: the prior implementation summed across all completed
// scan_results rows, so re-scanning the same manifest doubled (and tripled,
// quadrupled, …) the dashboard severity totals. The dedup pattern is the
// same one [[GetTenantSeverityBreakdown]] and [[GetSecurityOverview]] already
// use — kept consistent so a future column addition can't desync one of the
// three aggregators.
//
// Returns (total, critical, high, medium, low, negligible) where total is the
// sum of the five severity buckets.
func (r *Repository) GetTenantVulnerabilityCount(ctx context.Context, tenantID string) (total, critical, high, medium, low, negligible int64, err error) {
	const q = `
		WITH latest AS (
			SELECT DISTINCT ON (repo_id, manifest_digest) severity_counts
			FROM   scan_results
			WHERE  tenant_id = $1 AND status = 'complete'
			ORDER  BY repo_id, manifest_digest, completed_at DESC NULLS LAST, created_at DESC
		)
		SELECT
		  COALESCE(SUM((severity_counts->>'CRITICAL')::int),   0),
		  COALESCE(SUM((severity_counts->>'HIGH')::int),       0),
		  COALESCE(SUM((severity_counts->>'MEDIUM')::int),     0),
		  COALESCE(SUM((severity_counts->>'LOW')::int),        0),
		  COALESCE(SUM((severity_counts->>'NEGLIGIBLE')::int), 0)
		FROM latest`

	if err = r.reader().QueryRow(ctx, q, tenantID).Scan(&critical, &high, &medium, &low, &negligible); err != nil {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("GetTenantVulnerabilityCount: %w", err)
	}
	return critical + high + medium + low + negligible, critical, high, medium, low, negligible, nil
}
