// Package repository — test-only helpers for the FE-API-038 evaluator
// integration tests.
//
// These methods are exported because they're called from
// services/metadata/internal/testutil/integration which is a different
// package, so unexported helpers don't reach. They are NOT part of the
// production surface — the names embed "ForTest" so a code-review catches
// any drift toward calling them from non-test code.
//
// The helpers exist because the public Repository surface only exposes
// PutManifest, which stamps created_at = NOW(). The evaluator needs
// manifests at specific historical timestamps (so max_age_days /
// dangling_grace_days have anything to evaluate against), and we don't
// want to teach PutManifest a timestamp argument purely for tests.
package repository

import (
	"context"
	"fmt"
	"time"
)

// RawInsertManifestForTest inserts a manifest row at an explicit created_at
// and image_size_bytes — useful for the evaluator integration tests that
// need to seed manifests at historical timestamps with controlled sizes.
//
// The raw_json is set to a minimal valid OCI manifest skeleton so the
// NOT NULL constraint is satisfied. media_type can be any string.
//
// DO NOT call from production code. The function name embeds "ForTest" so
// a grep catches misuse during code review.
func (r *Repository) RawInsertManifestForTest(
	ctx context.Context,
	repoID, tenantID, digest, mediaType string,
	imageSizeBytes int64,
	createdAt time.Time,
) error {
	const q = `
		INSERT INTO manifests
		    (repo_id, tenant_id, digest, media_type, raw_json, size_bytes, image_size_bytes, created_at)
		VALUES
		    ($1, $2, $3, $4, $5, $6, $7, $8)`
	// raw_json must be non-NULL by the table constraint; the evaluator
	// doesn't inspect this column so a placeholder is sufficient.
	rawJSON := []byte(`{"schemaVersion":2}`)
	if _, err := r.pool.Exec(ctx, q, repoID, tenantID, digest, mediaType, rawJSON, int64(len(rawJSON)), imageSizeBytes, createdAt); err != nil {
		return fmt.Errorf("raw insert manifest for test: %w", err)
	}
	return nil
}

// InsertTenantForTest inserts a tenants row so integration tests can stand up
// a SECOND tenant alongside the seeded dev tenant. organizations.tenant_id has
// a FK to tenants(id), so any test that seeds an org under a fresh tenant must
// create the tenant row first. Idempotent via ON CONFLICT DO NOTHING so a test
// can call it defensively.
//
// DO NOT call from production code. The name embeds "ForTest" so a grep
// catches misuse during code review.
func (r *Repository) InsertTenantForTest(ctx context.Context, tenantID, name string) error {
	const q = `INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`
	if _, err := r.pool.Exec(ctx, q, tenantID, name); err != nil {
		return fmt.Errorf("insert tenant for test: %w", err)
	}
	return nil
}

// SetManifestLastPulledForTest stamps manifests.last_pulled_at directly for a
// given digest within a (tenant, repo). The production path is the FE-API-042
// 24h-debounced consumer; the FE-API-043 evaluator tests need to seed
// arbitrary historical pull timestamps (including NULL via the explicit
// nil-pointer caller path — but that's the default after RawInsertManifestForTest
// so this helper only covers the "set to a specific time" case).
//
// DO NOT call from production code.
func (r *Repository) SetManifestLastPulledForTest(
	ctx context.Context,
	repoID, tenantID, digest string,
	pulledAt time.Time,
) error {
	const q = `
		UPDATE manifests
		   SET last_pulled_at = $4
		 WHERE repo_id = $1
		   AND tenant_id = $2
		   AND digest = $3`
	if _, err := r.pool.Exec(ctx, q, repoID, tenantID, digest, pulledAt); err != nil {
		return fmt.Errorf("set last_pulled_at for test: %w", err)
	}
	return nil
}
