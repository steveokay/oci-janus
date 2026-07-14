// FE-API-042 — pull-activity tracking on manifests.last_pulled_at.
//
// The metadata pull.image consumer calls UpsertManifestLastPulledAt for every
// pull.image event landed by services/audit. A 24h debounce in the WHERE
// clause guarantees at most one Postgres write per (manifest, day) — hot
// manifests pulled thousands of times daily produce a single UPDATE per day
// rather than O(pulls) writes.
//
// Why debounce at the SQL layer (vs the consumer): the executor that runs
// the FE-API-043 max_idle_days rule only needs accuracy to within "have you
// been pulled in the last N days". A 24h granularity is plenty for that
// retention semantic and lets us skip an application-level cache that would
// complicate multi-instance deployments.
package repository

import (
	"context"
	"fmt"
	"time"
)

// UpsertManifestLastPulledAt stamps manifests.last_pulled_at = pulledAt if and
// only if the existing value is NULL or older than pulledAt - 24h. Returns
// the number of rows actually updated (0 when the row was already inside the
// 24h debounce window, or when the manifest doesn't belong to the tenant).
//
// The WHERE clause carries tenant_id so a poisoned pull.image event from one
// tenant cannot stamp another tenant's manifest. The metadata service is the
// last line of defence for tenant isolation on this path because the consumer
// trusts the event payload's manifest_id / digest lookup.
//
// Idempotency: re-running this with the same pulledAt is a no-op (the WHERE
// clause already returns 0 rows after the first apply within the debounce
// window). The consumer NACK/retry path is safe to re-deliver.
func (r *Repository) UpsertManifestLastPulledAt(ctx context.Context, manifestID, tenantID string, pulledAt time.Time) (int64, error) {
	// $3 is cast to timestamptz explicitly. Without the cast, Postgres infers
	// the type of the untyped `$3` from the `$3 - INTERVAL '24 hours'`
	// subexpression: because one operand is an interval, it resolves `$3` as
	// interval too, making the outer comparison `last_pulled_at (timestamptz)
	// < interval` — an operator that does not exist (SQLSTATE 42883). Pinning
	// $3::timestamptz forces `timestamptz - interval → timestamptz`, so the
	// comparison is timestamptz < timestamptz as intended (FUT-085).
	const q = `
		UPDATE manifests
		   SET last_pulled_at = $3
		 WHERE id = $1
		   AND tenant_id = $2
		   AND (last_pulled_at IS NULL OR last_pulled_at < $3::timestamptz - INTERVAL '24 hours')`

	tag, err := r.pool.Exec(ctx, q, manifestID, tenantID, pulledAt)
	if err != nil {
		return 0, fmt.Errorf("upsert last_pulled_at: %w", err)
	}
	return tag.RowsAffected(), nil
}

// FindManifestIDByDigest resolves a manifest UUID from (repo_id, digest, tenant_id).
// Used by the pull.image consumer when the event payload's manifest_id is empty —
// services/core does not always have it cached after the GET, so the consumer
// falls back to a digest lookup. ErrNotFound is returned when no manifest matches
// so the consumer can ACK + drop instead of NACK-retrying forever.
func (r *Repository) FindManifestIDByDigest(ctx context.Context, tenantID, repoID, digest string) (string, error) {
	const q = `
		SELECT id
		  FROM manifests
		 WHERE repo_id = $1
		   AND digest = $2
		   AND tenant_id = $3`

	var id string
	if err := r.pool.QueryRow(ctx, q, repoID, digest, tenantID).Scan(&id); err != nil {
		// pgx.ErrNoRows is wrapped to ErrNotFound by callers via errors.Is —
		// for symmetry with the rest of this repository we surface it raw and
		// let the consumer translate.
		return "", err
	}
	return id, nil
}
