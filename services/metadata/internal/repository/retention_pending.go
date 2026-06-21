// Package repository — FE-API-040 retention executor support.
//
// Three primitives backing the services/gc retention executor:
//
//	MarkManifestRetentionPending     — idempotent soft-delete write.
//	ClearManifestRetentionPending    — undo (UI affordance during grace).
//	ListPendingDeleteManifests       — past-grace lookup for the finaliser.
//
// All three tenant-scope on (manifest_id, tenant_id). The MarkPending column
// retention_pending_delete_at is owned exclusively by this package — no other
// code path writes it, so the executor controls the grace clock entirely.
//
// Idempotency on Mark is load-bearing: the retention sweep can re-run
// (operator triggers a second run, or the cron grace ticker fires while a
// previous sweep is still draining its candidate list). We MUST NOT bump the
// grace window every time a re-run sees the same manifest — that would mean
// a busy retention cron could indefinitely postpone the hard delete. The
// COALESCE in the UPDATE preserves any existing timestamp.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// MarkManifestRetentionPending stamps retention_pending_delete_at on a
// manifest the executor has decided is up for retention deletion. Idempotent
// — if the column is already set, the existing timestamp is preserved (a
// second Mark MUST NOT reset the grace clock).
//
// Returns ErrNotFound when no manifest matches (tenant_id, id). Mapped to
// gRPC NotFound by the handler.
func (r *Repository) MarkManifestRetentionPending(ctx context.Context, tenantID, manifestID string) error {
	// COALESCE keeps the existing timestamp on re-runs. NOW() is captured by
	// the database so a clock skew between the gc service and the metadata
	// service can't influence the grace window — the database is the
	// authoritative clock for the deadline arithmetic.
	const q = `
		UPDATE manifests
		   SET retention_pending_delete_at = COALESCE(retention_pending_delete_at, NOW())
		 WHERE id = $1
		   AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, manifestID, tenantID)
	if err != nil {
		return fmt.Errorf("mark retention pending: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearManifestRetentionPending unsets the column for one manifest. Used by
// the "undo" UI affordance during the grace window and by tests that need
// to reset state between cases. Returns ErrNotFound when no row matches.
func (r *Repository) ClearManifestRetentionPending(ctx context.Context, tenantID, manifestID string) error {
	const q = `
		UPDATE manifests
		   SET retention_pending_delete_at = NULL
		 WHERE id = $1
		   AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, manifestID, tenantID)
	if err != nil {
		return fmt.Errorf("clear retention pending: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPendingDeleteManifests returns manifests that have been pending past
// the supplied grace window. The deadline is computed server-side as
// NOW() - INTERVAL graceWindowSecs so any clock skew between caller and
// database goes the safe way (rows can only become eligible at the database
// clock's view of time).
//
// tenantID empty ⇒ scan every tenant. The cross-tenant grace ticker on
// services/gc uses this so the metadata-side query can drive both per-
// tenant and global passes through one RPC.
//
// limit is clamped to [1, MaxPendingDeleteLimit]. The handler is the
// canonical clamp point but we re-clamp defensively here so a misuse from
// the repo package directly can't load 500k rows.
func (r *Repository) ListPendingDeleteManifests(
	ctx context.Context,
	tenantID string,
	graceWindowSecs int64,
	limit int,
) ([]*metadatav1.PendingDeleteManifest, error) {
	if limit <= 0 {
		limit = DefaultPendingDeleteLimit
	}
	if limit > MaxPendingDeleteLimit {
		limit = MaxPendingDeleteLimit
	}
	if graceWindowSecs < 0 {
		graceWindowSecs = 0
	}

	// We pass graceWindowSecs as an interval-equivalent integer via
	// make_interval so a hostile caller can't slip a SQL fragment in via
	// fmt.Sprintf. make_interval(secs := $1::int) is parameterised end to
	// end and bounded by Postgres's interval handling.
	//
	// Use the read replica when configured — this is a read-heavy scan and
	// it's safe to read stale data here (the worst case is "we miss a
	// manifest that just became past-grace; next tick picks it up").
	const q = `
		SELECT id::text,
		       tenant_id::text,
		       repo_id::text,
		       digest,
		       image_size_bytes,
		       retention_pending_delete_at
		  FROM manifests
		 WHERE retention_pending_delete_at IS NOT NULL
		   AND retention_pending_delete_at < NOW() - make_interval(secs => $1::int)
		   AND ($2::TEXT = '' OR tenant_id::text = $2)
		 ORDER BY retention_pending_delete_at ASC
		 LIMIT $3`
	rows, err := r.reader().Query(ctx, q, graceWindowSecs, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending delete manifests: %w", err)
	}
	defer rows.Close()

	out := make([]*metadatav1.PendingDeleteManifest, 0, 16)
	for rows.Next() {
		var (
			id           string
			tid          string
			rid          string
			digest       string
			imageSize    int64
			pendingSince time.Time
		)
		if err := rows.Scan(&id, &tid, &rid, &digest, &imageSize, &pendingSince); err != nil {
			return nil, fmt.Errorf("scan pending row: %w", err)
		}
		out = append(out, &metadatav1.PendingDeleteManifest{
			ManifestId:   id,
			TenantId:     tid,
			RepositoryId: rid,
			Digest:       digest,
			SizeBytes:    imageSize,
			PendingSince: timestamppb.New(pendingSince),
		})
	}
	if err := rows.Err(); err != nil {
		// Distinguish pgx no-rows from an iteration error — only the latter is
		// surfaced. No-rows is the empty result + nil error path above.
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return nil, fmt.Errorf("iterate pending rows: %w", err)
	}
	return out, nil
}

// DefaultPendingDeleteLimit is the per-call cap the executor uses when the
// caller does not specify limit. 1000 is large enough that a typical
// retention sweep finishes in one pass but small enough that the grace
// ticker doesn't lock the index for an unreasonable time.
const DefaultPendingDeleteLimit = 1000

// MaxPendingDeleteLimit is the hard upper bound on a single call. Even if
// the handler is misconfigured we cap the result set to avoid a runaway
// scan.
const MaxPendingDeleteLimit = 5000
