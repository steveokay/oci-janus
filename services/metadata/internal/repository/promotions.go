package repository

// Package repository — promotions.go
//
// FUT-020 — image promotion. A promotion is the atomic act of copying a
// source tag's live manifest_digest onto a destination {org}/{repo}:{tag}.
// The read (source tag → digest), the upsert (destination tag), and the
// history INSERT all live inside ONE database transaction so a caller
// observes either every write or none. Rolling back on any error is the
// load-bearing invariant that prevents a "history row but no matching
// tag write" state from ever appearing on disk.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// PromoteTagInput is the resolved shape of a promotion request. All identity
// fields are string-form because the wire type is string; the tenant + actor
// UUIDs are parsed at the handler edge, not here, so a bad UUID surfaces as
// InvalidArgument on the gRPC side before the tx opens.
//
// ActorUserID is *uuid.UUID (rather than uuid.UUID) so the caller can pass
// nil for CLI/bot-driven promotions where the API-key owner is a service
// account with no human user attribution. Nil persists as NULL in
// promotions.actor_user_id.
type PromoteTagInput struct {
	TenantID    uuid.UUID
	SrcOrg      string
	SrcRepo     string
	SrcTag      string
	DstOrg      string
	DstRepo     string
	DstTag      string
	ActorUserID *uuid.UUID
	Note        string
}

// promoteFullName joins an org + repo pair into the "org/repo" composite
// key used by the metadata tables (repositories.name is the leaf only, but
// the composite name is what the tag → repository joins go through).
func promoteFullName(org, repo string) string { return org + "/" + repo }

// promotionSelectCols is the column list every Promotion row read shares.
// Kept as a constant so a new reader can't accidentally drop a field and
// silently return partial rows.
const promotionSelectCols = `id, tenant_id, src_org, src_repo, src_tag,
	src_digest, dst_org, dst_repo, dst_tag, dst_digest,
	COALESCE(actor_user_id::text, ''), COALESCE(note, ''), promoted_at`

// PromoteTag looks up the source manifest digest, upserts the destination
// tag pointing at the same digest, and records a promotions row — all in
// one transaction.
//
// Errors:
//   - ErrNotFound: source tag or destination repository is missing.
//   - ErrImmutableTag: destination tag exists at a DIFFERENT digest AND
//     either the repository is `immutable_tags=true` OR the tag is pinned
//     `immutable=true`. Wrapped as a distinct sentinel so the handler
//     maps it to gRPC FailedPrecondition instead of a generic Internal.
//   - Any other error is wrapped with %w so the caller can errors.Is-match
//     it against the sentinels or (via mapDBError) surface it as Internal.
//
// The re-promotion (source digest == existing destination digest) case is
// deliberately NOT treated as a no-op — it still writes a new promotions
// row so the audit trail records the operator's intent, but the tag row
// is updated in place with `updated_at = now()` and the digest is
// unchanged. Callers see the same success signal as a first-time promotion.
func (r *Repository) PromoteTag(ctx context.Context, in PromoteTagInput) (*metadatav1.Promotion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin promote-tag tx: %w", err)
	}
	// A rollback on a committed tx is a no-op — pgx documents this — so the
	// deferred rollback is safe in the success path too. On any error before
	// Commit, this is what unwinds every write.
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Resolve the source tag → live manifest_digest. Bounded to the
	//    tenant + source (org/repo) composite so a cross-tenant lookup can
	//    never succeed.
	srcFull := promoteFullName(in.SrcOrg, in.SrcRepo)
	const srcQ = `
		SELECT t.manifest_digest
		FROM tags t
		JOIN repositories r ON r.id = t.repo_id
		JOIN organizations o ON o.id = r.org_id
		WHERE t.tenant_id = $1
		  AND o.name || '/' || r.name = $2
		  AND t.name = $3`
	var srcDigest string
	if err := tx.QueryRow(ctx, srcQ, in.TenantID, srcFull, in.SrcTag).Scan(&srcDigest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve source tag: %w", err)
	}

	// 2. Resolve the destination repository. Fail closed on NotFound so a
	//    typo in the URL never accidentally creates a repo. The immutable
	//    flag comes back on the same row so we can enforce the immutability
	//    gate without a second round-trip.
	dstFull := promoteFullName(in.DstOrg, in.DstRepo)
	const dstRepoQ = `
		SELECT r.id, r.immutable_tags
		FROM repositories r
		JOIN organizations o ON o.id = r.org_id
		WHERE r.tenant_id = $1
		  AND o.name || '/' || r.name = $2`
	var dstRepoID string
	var dstRepoImmutable bool
	if err := tx.QueryRow(ctx, dstRepoQ, in.TenantID, dstFull).Scan(&dstRepoID, &dstRepoImmutable); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve destination repository: %w", err)
	}

	// 3. Check destination tag state — does it already exist? What digest?
	//    Is it individually pinned via `immutable=true`? Missing row is the
	//    common case and NOT an error (a promotion into a fresh tag name is
	//    the whole point).
	const dstTagQ = `
		SELECT manifest_digest, immutable
		FROM tags
		WHERE tenant_id = $1 AND repo_id = $2 AND name = $3`
	var existingDigest string
	var dstTagImmutable bool
	dstTagExists := true
	if err := tx.QueryRow(ctx, dstTagQ, in.TenantID, dstRepoID, in.DstTag).Scan(&existingDigest, &dstTagImmutable); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("resolve destination tag: %w", err)
		}
		dstTagExists = false
	}

	// 4. Immutability gate. Fires ONLY when the destination tag exists at
	//    a DIFFERENT digest — a re-promotion onto the same digest is
	//    idempotent (same-digest re-push is not a move, matching the
	//    checkTagImmutable precedent in services/core). The per-tag pin
	//    wins precedence over the repo-wide flag; either signal is
	//    sufficient to block a move.
	if dstTagExists && existingDigest != srcDigest && (dstRepoImmutable || dstTagImmutable) {
		return nil, ErrImmutableTag
	}

	// 5. Upsert the destination tag. Same shape as PutTag's inner query but
	//    without the manifest-join enrichment — that's only needed for the
	//    Tag proto return type, which the promotion path doesn't build.
	const upsertTagQ = `
		INSERT INTO tags (repo_id, tenant_id, name, manifest_digest)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (repo_id, name) DO UPDATE
		  SET manifest_digest = EXCLUDED.manifest_digest, updated_at = now()`
	if _, err := tx.Exec(ctx, upsertTagQ, dstRepoID, in.TenantID, in.DstTag, srcDigest); err != nil {
		return nil, fmt.Errorf("upsert destination tag: %w", err)
	}

	// 6. Record the promotions row. RETURNING gives us the server-side
	//    UUID + promoted_at without a second round-trip. actor_user_id is
	//    parameterised as a *uuid.UUID so nil persists as NULL.
	const insertPromotionQ = `
		INSERT INTO promotions (
			tenant_id, src_org, src_repo, src_tag, src_digest,
			dst_org, dst_repo, dst_tag, dst_digest,
			actor_user_id, note
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING ` + promotionSelectCols
	prom, err := scanPromotion(ctx, tx.QueryRow(ctx, insertPromotionQ,
		in.TenantID, in.SrcOrg, in.SrcRepo, in.SrcTag, srcDigest,
		in.DstOrg, in.DstRepo, in.DstTag, srcDigest, // dst_digest == srcDigest for the atomic-tag-copy shape
		in.ActorUserID, in.Note,
	))
	if err != nil {
		return nil, fmt.Errorf("insert promotion: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit promote-tag tx: %w", err)
	}
	return prom, nil
}

// ListPromotions returns recent promotions for the tenant, filtered by
// org/repo when supplied.
//
// A non-empty org (or org+repo) filter matches rows where EITHER the src
// or the dst side matches — the "promotions touching this repo" question
// wants both directions, otherwise the repo detail page would surface only
// half the history.
//
// limit is clamped to [1, 200] with a default of 50 when zero.
func (r *Repository) ListPromotions(ctx context.Context, tenantID uuid.UUID, org, repo string, limit int32) ([]*metadatav1.Promotion, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Build the predicate + args set. The tenant filter is always $1. The
	// org / repo filters are appended conditionally so an empty filter
	// falls through to a straight tenant-wide list.
	q := `SELECT ` + promotionSelectCols + ` FROM promotions WHERE tenant_id = $1`
	args := []any{tenantID}
	next := 2
	if org != "" {
		// Both sides matched — this is why the plan calls out the src OR dst
		// invariant. Same $N used twice via WHERE (src_org = $N OR dst_org = $N).
		q += fmt.Sprintf(" AND (src_org = $%d OR dst_org = $%d)", next, next)
		args = append(args, org)
		next++
	}
	if repo != "" {
		q += fmt.Sprintf(" AND (src_repo = $%d OR dst_repo = $%d)", next, next)
		args = append(args, repo)
		next++
	}
	q += fmt.Sprintf(" ORDER BY promoted_at DESC LIMIT $%d", next)
	args = append(args, limit)

	rows, err := r.reader().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
	}
	defer rows.Close()

	var out []*metadatav1.Promotion
	for rows.Next() {
		p, err := scanPromotion(ctx, rows)
		if err != nil {
			return nil, fmt.Errorf("scan promotion row: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// rowScanner is the intersection of *pgx.Row and pgx.Rows for our purposes:
// both expose Scan(dest ...any) error. Extracting a tiny interface here lets
// scanPromotion serve both the single-row RETURNING path and the multi-row
// list path with one implementation.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPromotion decodes one promotions row into the proto shape. The
// promoted_at column is time.Time on the pgx side and needs to be wrapped
// in a timestamppb.Timestamp for the wire type.
func scanPromotion(_ context.Context, row rowScanner) (*metadatav1.Promotion, error) {
	var p metadatav1.Promotion
	var promotedAt time.Time
	if err := row.Scan(
		&p.Id, &p.TenantId,
		&p.SrcOrg, &p.SrcRepo, &p.SrcTag, &p.SrcDigest,
		&p.DstOrg, &p.DstRepo, &p.DstTag, &p.DstDigest,
		&p.ActorUserId, &p.Note, &promotedAt,
	); err != nil {
		return nil, err
	}
	p.PromotedAt = timestamppb.New(promotedAt)
	return &p, nil
}
