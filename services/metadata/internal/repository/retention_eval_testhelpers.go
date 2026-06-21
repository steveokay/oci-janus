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
