// Package repository — FE-API-039 per-org default retention policy.
//
// Four repository methods backing the metadata gRPC handler:
//
//	GetOrgRetentionPolicy        — returns ErrNotFound when no row.
//	UpsertOrgRetentionPolicy     — single UPSERT, preview_until via shared helper.
//	DeleteOrgRetentionPolicy     — returns ErrNotFound when 0 rows.
//	GetEffectiveRetentionPolicy  — per-repo first, then org default (only when
//	                                enabled), else ErrNotFound.
//
// Inheritance rule: the org default is only used when its `enabled = TRUE`.
// A disabled org default does NOT propagate — falling back to a disabled
// policy would surface a confusing "default exists but doesn't enforce"
// banner in the UI. The BFF treats "default disabled + no per-repo" the
// same as "no policy anywhere".
//
// We use TWO sequential queries (per-repo, then org default) rather than a
// single CTE/UNION because:
//   - The per-repo lookup is the hot path (every effective lookup); the org
//     default fallback is rare. Returning early avoids the second query in
//     the common case.
//   - The CTE form needs a placeholder cast to align repo-row columns with
//     org-default columns in the UNION ALL branches — easy to mis-type.
//   - Splitting keeps each SQL statement readable and individually
//     EXPLAIN-able under load.
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

// EffectivePolicyResult is the in-Go shape of EffectiveRetentionPolicy.
// We keep it as a Go struct (not the raw proto) so the handler is the single
// place that performs the proto conversion — same pattern as
// EvaluationResult for the FE-API-038 evaluator.
type EffectivePolicyResult struct {
	// Policy is the resolved retention policy. RepoId is set when
	// InheritedFrom == "repo"; OrgId is set when InheritedFrom == "org".
	Policy *metadatav1.RetentionPolicy
	// InheritedFrom is "repo" when the per-repo row was used, "org" when
	// the org default was used. Always one of the two when err == nil.
	InheritedFrom string
	// OrgID carries the org_id of the org that owns the default policy
	// (populated only when InheritedFrom == "org"). Empty otherwise.
	OrgID string
}

// GetOrgRetentionPolicy returns the default policy row for (org_id, tenant_id)
// or ErrNotFound. Mirrors GetRepoRetentionPolicy but reads from
// retention_policy_defaults; the response message reuses RetentionPolicy
// (repo_id empty, org_id populated) so the handler can return it unchanged.
func (r *Repository) GetOrgRetentionPolicy(ctx context.Context, tenantID, orgID string) (*metadatav1.RetentionPolicy, error) {
	const q = `
		SELECT org_id::text,
		       tenant_id::text,
		       enabled,
		       rules,
		       protected_tag_patterns,
		       preview_until,
		       created_at,
		       updated_at,
		       updated_by::text
		FROM   retention_policy_defaults
		WHERE  org_id = $1 AND tenant_id = $2`

	var (
		policy       metadatav1.RetentionPolicy
		rulesJSON    []byte
		patterns     []string
		previewUntil *time.Time
		createdAt    time.Time
		updatedAt    time.Time
		updatedByPtr *string
	)
	err := r.pool.QueryRow(ctx, q, orgID, tenantID).Scan(
		&policy.OrgId,
		&policy.TenantId,
		&policy.Enabled,
		&rulesJSON,
		&patterns,
		&previewUntil,
		&createdAt,
		&updatedAt,
		&updatedByPtr,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get org retention policy: %w", err)
	}

	rules, err := decodeRetentionRules(rulesJSON)
	if err != nil {
		return nil, fmt.Errorf("decode org retention rules: %w", err)
	}
	policy.Rules = rules
	policy.ProtectedTagPatterns = patterns
	if previewUntil != nil {
		policy.PreviewUntil = timestamppb.New(*previewUntil)
	}
	policy.CreatedAt = timestamppb.New(createdAt)
	policy.UpdatedAt = timestamppb.New(updatedAt)
	if updatedByPtr != nil {
		policy.UpdatedBy = *updatedByPtr
	}
	return &policy, nil
}

// UpsertOrgRetentionPolicy creates or updates the org default. preview_until
// semantics are identical to UpsertRepoRetentionPolicy and share the
// decidePreviewUntil helper so the two paths cannot drift apart.
//
// FK violations on org_id (org deleted between BFF lookup and this call)
// surface as ErrNotFound so the handler maps to gRPC NotFound.
func (r *Repository) UpsertOrgRetentionPolicy(
	ctx context.Context,
	tenantID, orgID string,
	enabled bool,
	rules []*metadatav1.RetentionRule,
	protectedPatterns []string,
	updatedBy string,
) (*metadatav1.RetentionPolicy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin upsert org tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Snapshot the prior row inside the transaction so the preview_until
	// decision sees a consistent view. ErrNoRows ⇒ fresh insert.
	var (
		priorEnabled      bool
		priorRulesJSON    []byte
		priorPreviewUntil *time.Time
		priorExists       bool
	)
	const selectPrior = `
		SELECT enabled, rules, preview_until
		FROM   retention_policy_defaults
		WHERE  org_id = $1 AND tenant_id = $2
		FOR UPDATE`
	err = tx.QueryRow(ctx, selectPrior, orgID, tenantID).Scan(&priorEnabled, &priorRulesJSON, &priorPreviewUntil)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		priorExists = false
	case err != nil:
		return nil, fmt.Errorf("select prior org retention policy: %w", err)
	default:
		priorExists = true
	}

	rulesJSON, err := encodeRetentionRules(rules)
	if err != nil {
		return nil, fmt.Errorf("encode org retention rules: %w", err)
	}
	newPreviewUntil, err := decidePreviewUntil(enabled, priorExists, priorEnabled, priorRulesJSON, rulesJSON, priorPreviewUntil)
	if err != nil {
		return nil, err
	}

	// Normalise empty patterns to a non-nil slice for the TEXT[] column.
	patterns := protectedPatterns
	if patterns == nil {
		patterns = []string{}
	}

	// updated_by: empty string ⇒ NULL (system writer); UUID otherwise.
	var updatedByArg any
	if updatedBy == "" {
		updatedByArg = nil
	} else {
		updatedByArg = updatedBy
	}

	const upsertQ = `
		INSERT INTO retention_policy_defaults (
		    org_id, tenant_id, enabled, rules, protected_tag_patterns,
		    preview_until, updated_at, updated_by
		)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
		ON CONFLICT (org_id) DO UPDATE
		   SET enabled                = EXCLUDED.enabled,
		       rules                  = EXCLUDED.rules,
		       protected_tag_patterns = EXCLUDED.protected_tag_patterns,
		       preview_until          = EXCLUDED.preview_until,
		       updated_at             = NOW(),
		       updated_by             = EXCLUDED.updated_by
		RETURNING org_id::text, tenant_id::text, enabled, rules,
		          protected_tag_patterns, preview_until, created_at,
		          updated_at, updated_by::text`

	var (
		policy       metadatav1.RetentionPolicy
		outRulesJSON []byte
		outPatterns  []string
		outPreview   *time.Time
		createdAt    time.Time
		updatedAt    time.Time
		outUpdatedBy *string
	)
	err = tx.QueryRow(ctx, upsertQ,
		orgID, tenantID, enabled, rulesJSON, patterns, newPreviewUntil, updatedByArg,
	).Scan(
		&policy.OrgId,
		&policy.TenantId,
		&policy.Enabled,
		&outRulesJSON,
		&outPatterns,
		&outPreview,
		&createdAt,
		&updatedAt,
		&outUpdatedBy,
	)
	if err != nil {
		// Map FK violation on org_id (23503) to ErrNotFound so a deleted
		// org surfaces as gRPC NotFound rather than a 500. Same pattern as
		// UpsertRepoRetentionPolicy.
		if isForeignKeyViolation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("upsert org retention policy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit upsert org: %w", err)
	}

	outRules, err := decodeRetentionRules(outRulesJSON)
	if err != nil {
		return nil, fmt.Errorf("decode org upsert response rules: %w", err)
	}
	policy.Rules = outRules
	policy.ProtectedTagPatterns = outPatterns
	if outPreview != nil {
		policy.PreviewUntil = timestamppb.New(*outPreview)
	}
	policy.CreatedAt = timestamppb.New(createdAt)
	policy.UpdatedAt = timestamppb.New(updatedAt)
	if outUpdatedBy != nil {
		policy.UpdatedBy = *outUpdatedBy
	}
	return &policy, nil
}

// DeleteOrgRetentionPolicy removes the org default. Returns ErrNotFound when
// no row exists for (org_id, tenant_id). Repos that previously inherited
// fall back to "no policy" (the GetEffective path returns NotFound).
func (r *Repository) DeleteOrgRetentionPolicy(ctx context.Context, tenantID, orgID string) error {
	const q = `DELETE FROM retention_policy_defaults WHERE org_id = $1 AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, orgID, tenantID)
	if err != nil {
		return fmt.Errorf("delete org retention policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetEffectiveRetentionPolicy returns the policy that would apply to a
// repository RIGHT NOW. The resolution order is:
//
//  1. Per-repo row in retention_policies (regardless of enabled — a disabled
//     per-repo override is still the authoritative answer, NOT a signal to
//     fall back).
//  2. Org default in retention_policy_defaults, but ONLY when its
//     enabled = TRUE. A disabled default is treated as "no policy".
//  3. ErrNotFound — neither layer has a row.
//
// Two sequential queries — the per-repo hit is the hot path and short-circuits
// the second lookup. See the package doc for the trade-off rationale.
func (r *Repository) GetEffectiveRetentionPolicy(ctx context.Context, tenantID, repoID string) (*EffectivePolicyResult, error) {
	// Step 1 — per-repo. Any policy here wins, enabled or not. We could
	// call GetRepoRetentionPolicy verbatim, but inlining the SELECT avoids
	// the extra context allocation + lets us scope the columns explicitly.
	repoPolicy, err := r.GetRepoRetentionPolicy(ctx, tenantID, repoID)
	switch {
	case err == nil:
		return &EffectivePolicyResult{Policy: repoPolicy, InheritedFrom: "repo"}, nil
	case errors.Is(err, ErrNotFound):
		// Fall through to the org-default lookup.
	default:
		return nil, err
	}

	// Step 2 — org default. We need the repo's parent org_id, and we filter
	// on `enabled = TRUE` so a disabled default does not propagate. The JOIN
	// against repositories also enforces tenant isolation: a repo from
	// tenant A cannot resolve to an org default from tenant B because the
	// repository row carries both tenant_id and org_id.
	const q = `
		SELECT d.org_id::text,
		       d.tenant_id::text,
		       d.enabled,
		       d.rules,
		       d.protected_tag_patterns,
		       d.preview_until,
		       d.created_at,
		       d.updated_at,
		       d.updated_by::text
		FROM   retention_policy_defaults d
		JOIN   repositories r ON r.org_id = d.org_id
		WHERE  r.id        = $1
		  AND  r.tenant_id = $2
		  AND  d.enabled   = TRUE`

	var (
		policy       metadatav1.RetentionPolicy
		rulesJSON    []byte
		patterns     []string
		previewUntil *time.Time
		createdAt    time.Time
		updatedAt    time.Time
		updatedByPtr *string
	)
	err = r.pool.QueryRow(ctx, q, repoID, tenantID).Scan(
		&policy.OrgId,
		&policy.TenantId,
		&policy.Enabled,
		&rulesJSON,
		&patterns,
		&previewUntil,
		&createdAt,
		&updatedAt,
		&updatedByPtr,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get effective retention policy: %w", err)
	}

	rules, err := decodeRetentionRules(rulesJSON)
	if err != nil {
		return nil, fmt.Errorf("decode effective retention rules: %w", err)
	}
	policy.Rules = rules
	policy.ProtectedTagPatterns = patterns
	if previewUntil != nil {
		policy.PreviewUntil = timestamppb.New(*previewUntil)
	}
	policy.CreatedAt = timestamppb.New(createdAt)
	policy.UpdatedAt = timestamppb.New(updatedAt)
	if updatedByPtr != nil {
		policy.UpdatedBy = *updatedByPtr
	}
	return &EffectivePolicyResult{
		Policy:        &policy,
		InheritedFrom: "org",
		OrgID:         policy.OrgId,
	}, nil
}

// isForeignKeyViolation extracts the 23503 (foreign_key_violation) check from
// the per-repo upsert into a small helper so both call sites stay in sync.
// Defined here rather than in retention.go because retention_org.go is the
// younger file — we'll fold the per-repo path onto this helper if/when we
// next touch it.
func isForeignKeyViolation(err error) bool {
	type pgErr interface {
		SQLState() string
	}
	var e pgErr
	if errors.As(err, &e) {
		return e.SQLState() == "23503"
	}
	return false
}
