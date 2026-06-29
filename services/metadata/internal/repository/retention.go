// Package repository — FE-API-037 per-repo retention policy CRUD.
//
// Three repository methods backing the metadata gRPC handler:
//
//	GetRepoRetentionPolicy     — returns ErrNotFound when no row.
//	UpsertRepoRetentionPolicy  — single UPSERT with preview_until semantics.
//	DeleteRepoRetentionPolicy  — returns ErrNotFound when 0 rows affected.
//
// All queries are tenant-scoped on both the WHERE clause and the JOIN-by-PK
// path so RLS + application-level isolation hold.
//
// preview_until is owned by this layer, not the caller. The Upsert sets it to
// NOW() + 24h whenever the policy transitions from disabled→enabled OR the
// rules change materially while already enabled. The "material change" check
// compares the previous rule set deep so a no-op re-save (same rules + same
// enabled=true) doesn't keep restarting the 24h preview window — which would
// be infinite-preview behaviour the executor (FE-API-040) couldn't ever break
// out of.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// retentionPreviewWindow is the duration applied to preview_until when a
// policy is freshly enabled or its rules change materially. 24h matches the
// FE-API-038 spec — long enough for an operator to inspect the dry-run
// results before the executor (FE-API-040) starts deleting, short enough that
// a forgotten preview window doesn't permanently disable enforcement.
const retentionPreviewWindow = 24 * time.Hour

// retentionRule is the JSON shape persisted in retention_policies.rules. We
// keep this private to the repository so the gRPC layer maps directly between
// the proto RetentionRule and the JSONB column without exposing a third type.
type retentionRule struct {
	Kind  string `json:"kind"`
	Value int64  `json:"value"`
}

// GetRepoRetentionPolicy returns the policy row for (repo_id, tenant_id) or
// ErrNotFound when the row does not exist. The handler maps ErrNotFound to
// gRPC NotFound; the management BFF then falls back to the org default
// (FE-API-039, separate ticket).
//
// but reads a different table (repo vs org defaults) and a different scoping
// column; intentional duplication for type-safe scope handling.
//
//nolint:dupl // Structurally similar to GetOrgRetentionPolicy in retention_org.go
func (r *Repository) GetRepoRetentionPolicy(ctx context.Context, tenantID, repoID string) (*metadatav1.RetentionPolicy, error) {
	const q = `
		SELECT repo_id::text,
		       tenant_id::text,
		       enabled,
		       rules,
		       protected_tag_patterns,
		       preview_until,
		       created_at,
		       updated_at,
		       updated_by::text
		FROM   retention_policies
		WHERE  repo_id = $1 AND tenant_id = $2`

	var (
		policy       metadatav1.RetentionPolicy
		rulesJSON    []byte
		patterns     []string
		previewUntil *time.Time
		createdAt    time.Time
		updatedAt    time.Time
		updatedByPtr *string
	)
	err := r.pool.QueryRow(ctx, q, repoID, tenantID).Scan(
		&policy.RepoId,
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
		return nil, fmt.Errorf("get retention policy: %w", err)
	}

	rules, err := decodeRetentionRules(rulesJSON)
	if err != nil {
		return nil, fmt.Errorf("decode retention rules: %w", err)
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

// UpsertRepoRetentionPolicy creates or updates a retention policy row. The
// preview_until column is owned by this method:
//
//   - If no prior row existed and `enabled=true` is requested, preview_until
//     is set to NOW() + retentionPreviewWindow.
//   - If a prior row existed with enabled=false and the new row has
//     enabled=true, preview_until is set to NOW() + retentionPreviewWindow.
//   - If a prior row existed with enabled=true and the new row has the same
//     enabled=true with a different rules set, preview_until is reset to
//     NOW() + retentionPreviewWindow so the dry-run window covers the new
//     rules before enforcement.
//   - Otherwise preview_until is preserved as-is.
//
// The "rules changed materially" check sorts rules by kind before comparing so
// re-ordering the same set is not treated as a change.
//
// FK violations on repo_id (the repo was deleted between BFF lookup and this
// call) surface as ErrNotFound so the handler can map to gRPC NotFound rather
// than a 500.
func (r *Repository) UpsertRepoRetentionPolicy(
	ctx context.Context,
	tenantID, repoID string,
	enabled bool,
	rules []*metadatav1.RetentionRule,
	protectedPatterns []string,
	updatedBy string,
) (*metadatav1.RetentionPolicy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin upsert tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Fetch the prior row (if any) inside the transaction so the preview_until
	// decision is based on a consistent snapshot. ErrNoRows is a fresh insert.
	var (
		priorEnabled      bool
		priorRulesJSON    []byte
		priorPreviewUntil *time.Time
		priorExists       bool
	)
	const selectPrior = `
		SELECT enabled, rules, preview_until
		FROM   retention_policies
		WHERE  repo_id = $1 AND tenant_id = $2
		FOR UPDATE`
	err = tx.QueryRow(ctx, selectPrior, repoID, tenantID).Scan(&priorEnabled, &priorRulesJSON, &priorPreviewUntil)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		priorExists = false
	case err != nil:
		return nil, fmt.Errorf("select prior retention policy: %w", err)
	default:
		priorExists = true
	}

	// Decide preview_until via the shared helper so the org-default upsert
	// (FE-API-039) reuses the same enable-flip + material-rules-change rules.
	rulesJSON, err := encodeRetentionRules(rules)
	if err != nil {
		return nil, fmt.Errorf("encode retention rules: %w", err)
	}
	newPreviewUntil, err := decidePreviewUntil(enabled, priorExists, priorEnabled, priorRulesJSON, rulesJSON, priorPreviewUntil)
	if err != nil {
		return nil, err
	}

	// Normalise patterns to a non-nil slice so the TEXT[] column does not
	// receive a SQL NULL (it has NOT NULL DEFAULT '...'). The handler is
	// responsible for falling back to the table default when the caller
	// supplies nil/empty.
	patterns := protectedPatterns
	if patterns == nil {
		patterns = []string{}
	}

	// updated_by is a UUID column; pgx will marshal a non-empty string as a
	// UUID. An empty string is a NULL — covers the "system" updater case.
	var updatedByArg any
	if updatedBy == "" {
		updatedByArg = nil
	} else {
		updatedByArg = updatedBy
	}

	const upsertQ = `
		INSERT INTO retention_policies (
		    repo_id, tenant_id, enabled, rules, protected_tag_patterns,
		    preview_until, updated_at, updated_by
		)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
		ON CONFLICT (repo_id) DO UPDATE
		   SET enabled                = EXCLUDED.enabled,
		       rules                  = EXCLUDED.rules,
		       protected_tag_patterns = EXCLUDED.protected_tag_patterns,
		       preview_until          = EXCLUDED.preview_until,
		       updated_at             = NOW(),
		       updated_by             = EXCLUDED.updated_by
		RETURNING repo_id::text, tenant_id::text, enabled, rules,
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
		repoID, tenantID, enabled, rulesJSON, patterns, newPreviewUntil, updatedByArg,
	).Scan(
		&policy.RepoId,
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
		// 23503 is foreign_key_violation — the repo_id reference is invalid
		// because the repo was deleted (or never existed). Surface as
		// ErrNotFound so the handler returns gRPC NotFound rather than 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("upsert retention policy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit upsert: %w", err)
	}

	outRules, err := decodeRetentionRules(outRulesJSON)
	if err != nil {
		return nil, fmt.Errorf("decode upsert response rules: %w", err)
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

// DeleteRepoRetentionPolicy removes the per-repo retention policy. Returns
// ErrNotFound when no row exists for (repo_id, tenant_id), which the handler
// maps to gRPC NotFound so the BFF returns 404. The repo itself is unaffected.
func (r *Repository) DeleteRepoRetentionPolicy(ctx context.Context, tenantID, repoID string) error {
	const q = `DELETE FROM retention_policies WHERE repo_id = $1 AND tenant_id = $2`
	tag, err := r.pool.Exec(ctx, q, repoID, tenantID)
	if err != nil {
		return fmt.Errorf("delete retention policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// encodeRetentionRules serialises the proto rule slice into the JSONB shape we
// persist. We strip the proto wire fields and write just {kind, value} so the
// column stays a clean list of dictionaries — easy to read from psql and
// trivial to migrate to a Postgres array of composite types later if needed.
func encodeRetentionRules(rules []*metadatav1.RetentionRule) ([]byte, error) {
	out := make([]retentionRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, retentionRule{Kind: r.GetKind(), Value: r.GetValue()})
	}
	return json.Marshal(out)
}

// decodeRetentionRules is the inverse — the column's JSONB array is mapped
// back into proto RetentionRule pointers in the order it was stored.
func decodeRetentionRules(raw []byte) ([]*metadatav1.RetentionRule, error) {
	if len(raw) == 0 {
		return []*metadatav1.RetentionRule{}, nil
	}
	var rules []retentionRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	out := make([]*metadatav1.RetentionRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, &metadatav1.RetentionRule{Kind: r.Kind, Value: r.Value})
	}
	return out, nil
}

// rulesChangedMaterially returns true when the JSON-encoded prior rule set
// differs from the JSON-encoded new rule set after sorting by (kind, value).
// Sorting makes re-ordering a no-op — the executor doesn't care about order.
// Returns an error only if either side fails to decode (corrupt persisted
// data); callers treat decode failure as "rules changed" implicitly because
// they bubble the error up to the caller.
func rulesChangedMaterially(priorJSON, newJSON []byte) (bool, error) {
	prior, err := normaliseRulesForComparison(priorJSON)
	if err != nil {
		return false, fmt.Errorf("decode prior rules for comparison: %w", err)
	}
	next, err := normaliseRulesForComparison(newJSON)
	if err != nil {
		return false, fmt.Errorf("decode new rules for comparison: %w", err)
	}
	if len(prior) != len(next) {
		return true, nil
	}
	for i := range prior {
		if prior[i] != next[i] {
			return true, nil
		}
	}
	return false, nil
}

// decidePreviewUntil centralises the preview-window state machine shared
// between per-repo (FE-API-037) and per-org-default (FE-API-039) upserts.
//
// The rules are:
//   - enabled = false                ⇒ clear (preview gates enforcement; with
//     enforcement off there is nothing to gate).
//   - enabled = true, no prior row   ⇒ NOW() + retentionPreviewWindow.
//   - enabled = true, prior disabled ⇒ NOW() + retentionPreviewWindow.
//   - enabled = true, prior enabled  ⇒ if rules changed materially, restart
//     the window; otherwise preserve whatever was there (which may be nil
//     or in the past — either way the upsert must not silently restart
//     enforcement gating for a no-op re-save).
//
// `priorRulesJSON` and `newRulesJSON` are the same JSONB shape persisted by
// encodeRetentionRules — feeding raw JSON in keeps the helper pure (it does
// not need a pgx connection) and matches how the callers already have the
// data on hand.
func decidePreviewUntil(
	enabled, priorExists, priorEnabled bool,
	priorRulesJSON, newRulesJSON []byte,
	priorPreviewUntil *time.Time,
) (*time.Time, error) {
	if !enabled {
		// Disabled — no preview gating needed. Always nil so the column
		// stays truthful for the executor.
		return nil, nil
	}
	previewExpiry := time.Now().Add(retentionPreviewWindow)
	if !priorExists || !priorEnabled {
		// Fresh insert OR disabled→enabled transition — start the window.
		return &previewExpiry, nil
	}
	// Both prior + new are enabled. Inspect the rule set: a material change
	// restarts the window so the new rules get their own 24h dry-run; a no-op
	// re-save preserves the existing preview_until.
	changed, err := rulesChangedMaterially(priorRulesJSON, newRulesJSON)
	if err != nil {
		return nil, err
	}
	if changed {
		return &previewExpiry, nil
	}
	return priorPreviewUntil, nil
}

// normaliseRulesForComparison decodes a JSONB rules blob into a deterministic
// slice for equality testing. The handler guarantees each kind appears at most
// once so the (kind, value) sort key is unique.
func normaliseRulesForComparison(raw []byte) ([]retentionRule, error) {
	if len(raw) == 0 {
		return []retentionRule{}, nil
	}
	var rules []retentionRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Kind != rules[j].Kind {
			return rules[i].Kind < rules[j].Kind
		}
		return rules[i].Value < rules[j].Value
	})
	return rules, nil
}
