// Package repository — FE-API-038 retention policy evaluator.
//
// Read-only evaluation of a candidate retention policy against the current
// (manifests, tags) state of a repository. Neither the candidate policy nor
// the evaluation result is persisted — the executor (FE-API-040, separate
// ticket) is the only path that actually deletes anything.
//
// Algorithm in one paragraph:
//
//  1. Load every manifest in the repo joined with its tag names. The manifest
//     list is ordered DESC by created_at so the max_count / max_size_bytes
//     rules can "keep the newest N" deterministically.
//  2. Compile each protected_tag_pattern once.
//  3. For each manifest: if any tag matches any pattern → protected_skipped
//     (carry the first matched pattern). Otherwise run each rule and collect
//     every rule kind that selects it into reasons[]. Non-empty reasons →
//     would_delete with all matching kinds reported back, so the UI surfaces
//     "this manifest matches both max_age_days(90) AND max_count(50)" rather
//     than a single arbitrary winner.
//  4. Cap would_delete to maxDeleteResults (default 1000, server-clamped to
//     ≤5000). total_count / total_bytes always reflect the full set, computed
//     before the slice is truncated.
//  5. Cap protected_skipped to maxProtectedResults (default 100, server-
//     clamped to ≤500). No truncation flag is exposed — the UI doesn't
//     render protected-skipped as a primary affordance.
//
// Schema gaps:
//
//   - We don't track tag-removal time, so dangling_grace_days falls back to
//     "no tags AND created_at older than N days" — i.e. uses the manifest's
//     own created_at as the proxy for "has been dangling for at least N days
//     from the operator's perspective." When FE-API-040's executor lands it
//     can do better by writing a tag-removed-at column.
//   - max_idle_days (FE-API-043) uses manifests.last_pulled_at (populated by
//     the FE-API-042 24h-debounced pull consumer) gated by a combined
//     created_at + last_pulled_at predicate — see buildMaxIdleSet for the
//     full rationale on why the gate is mandatory.
package repository

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// EvaluationResult is the in-Go shape of EvaluateRetentionResponse. We keep
// it separate from the proto so the handler is the single place that performs
// the proto conversion — same pattern as repository.SecurityOverview.
type EvaluationResult struct {
	// WouldDelete is the truncated list of selection candidates. Each entry
	// carries every rule kind that selected it, sorted ascending — see
	// the deterministic-ordering note on EvaluateRetention.
	WouldDelete []EvaluationCandidate
	// ProtectedSkipped is the truncated list of manifests excluded because
	// at least one tag matched a protected_tag_patterns regex.
	ProtectedSkipped []EvaluationProtected
	// TotalCount / TotalBytes always reflect the FULL would-delete set,
	// not the truncated WouldDelete slice. Computed in pure Go after
	// classification rather than via a separate SQL aggregate so the
	// evaluation is a single read.
	TotalCount int64
	TotalBytes int64
	// EvaluatedAt is the timestamp the evaluator captured at call entry —
	// the same `now` used by every rule's deadline arithmetic so the result
	// is internally consistent.
	EvaluatedAt time.Time
	// Truncated is true when WouldDelete was capped by maxDeleteResults.
	// The protected-skipped truncation has no flag (the UI doesn't need it).
	Truncated bool
}

// EvaluationCandidate is the in-Go shape of RetentionDeletionCandidate.
type EvaluationCandidate struct {
	ManifestID     string
	ManifestDigest string
	Tags           []string
	PushedAt       time.Time
	SizeBytes      int64
	// Reasons is the list of rule kinds that selected this manifest, sorted
	// ascending so the wire shape is stable across runs (and across
	// composition orderings — max_age_days + max_count produces the same
	// reasons slice no matter which rule fires first internally).
	Reasons []string
}

// EvaluationProtected is the in-Go shape of RetentionProtectedManifest.
type EvaluationProtected struct {
	ManifestID     string
	ManifestDigest string
	Tags           []string
	// MatchedPattern is the FIRST protected_tag_pattern that matched one of
	// this manifest's tags. We don't echo all matches because the UI just
	// needs to render "kept: tag X matched Y".
	MatchedPattern string
}

// EvaluationCaps are the default + maximum caps the handler applies before
// calling EvaluateRetention. Exported so the handler can keep the clamping
// constants in one place (and tests can assert against the same values).
const (
	DefaultMaxDeleteResults    = 1000
	MaxMaxDeleteResults        = 5000
	DefaultMaxProtectedResults = 100
	MaxMaxProtectedResults     = 500
)

// retentionRuleKind enumerates the rule kinds the evaluator recognises.
// Centralised here (not in the handler) so a future kind landing in the
// SQL enum without an evaluator branch surfaces as a compile error rather
// than a silent ignore.
const (
	ruleKindMaxAgeDays        = "max_age_days"
	ruleKindMaxCount          = "max_count"
	ruleKindMaxSizeBytes      = "max_size_bytes"
	ruleKindDanglingGraceDays = "dangling_grace_days"
	ruleKindMaxIdleDays       = "max_idle_days"
)

// evalManifest is the per-row decoded shape produced by the SQL load step.
// It carries enough context to feed into the rule passes without keeping
// the result-set alive — the slice of evalManifest IS the working set.
//
// lastPulledAt is a pointer so the NULL-vs-zero distinction survives into
// Go: a non-nil zero time would be indistinguishable from "never pulled",
// and max_idle_days' chicken-and-egg gate (see buildMaxIdleSet) treats NULL
// distinctly. The pointer is nil when the column is NULL on disk.
type evalManifest struct {
	manifestID   string
	digest       string
	sizeBytes    int64
	createdAt    time.Time
	lastPulledAt *time.Time
	tags         []string
}

// EvaluateRetention materialises the would-delete + protected-skipped sets
// for `candidate` against the current state of `repoID` within `tenantID`.
//
// The candidate is treated as authoritative wire data — the caller (handler)
// has already validated kinds + values + regex compilability via the same
// helpers UpsertRepoRetentionPolicy uses. The evaluator still recovers from
// regex compile panics defensively so a corrupted persisted policy can't
// take the metadata process down.
//
// `maxDeleteResults` / `maxProtectedResults` ≤ 0 fall back to the defaults
// declared above; the handler applies the hard caps. Passing zero or
// negative here is a programming bug not exposed on the wire.
//
// The repo-not-found / empty-repo distinction is the caller's concern: an
// empty repo returns an empty result with TotalCount=0 (not an error),
// matching the spec.
func (r *Repository) EvaluateRetention(
	ctx context.Context,
	tenantID, repoID string,
	candidate *metadatav1.RetentionPolicyCandidate,
	maxDeleteResults, maxProtectedResults int,
) (*EvaluationResult, error) {
	if maxDeleteResults <= 0 {
		maxDeleteResults = DefaultMaxDeleteResults
	}
	if maxProtectedResults <= 0 {
		maxProtectedResults = DefaultMaxProtectedResults
	}

	// Single capture of "now" so every rule's deadline arithmetic shares one
	// epoch. Returned in EvaluatedAt so the BFF/UI can surface it.
	now := time.Now().UTC()

	manifests, err := r.loadManifestsForEval(ctx, tenantID, repoID)
	if err != nil {
		return nil, fmt.Errorf("load manifests for eval: %w", err)
	}

	// Compile protected_tag_patterns once. The handler validates these on
	// the write path so a successful EvaluateRetention call should never see
	// a bad pattern; we still recover from a runtime panic if a future
	// schema change lets a malformed pattern through.
	patterns, err := compileProtectedPatterns(candidate.GetProtectedTagPatterns())
	if err != nil {
		return nil, fmt.Errorf("compile protected patterns: %w", err)
	}

	// Pre-compute the per-rule selection sets that depend on the WHOLE
	// non-protected ordering (max_count, max_size_bytes). These rules pick
	// "what to keep" against the entire candidate set, NOT against the
	// surviving-other-rule set — the spec is explicit about this and it
	// matters for composition. Example: with max_count=10 the rule selects
	// indices 10.. of the chronologically-sorted set regardless of which
	// of those a max_age_days rule also matched, so a manifest matched by
	// both rules reports both kinds in reasons[].
	rulesByKind := indexRules(candidate.GetRules())

	// Build the per-rule index sets. We use indices into `manifests` rather
	// than digests so the lookup is O(1) per check.
	maxCountSelected := buildMaxCountSet(manifests, rulesByKind[ruleKindMaxCount])
	maxSizeSelected := buildMaxSizeSet(manifests, rulesByKind[ruleKindMaxSizeBytes])

	// Walk every manifest. Classification is single-pass so the result
	// ordering matches the SQL ordering (created_at DESC) before we
	// re-sort for stable wire output below.
	var (
		fullDelete       []EvaluationCandidate
		protectedOut     []EvaluationProtected
		totalCount       int64
		totalBytes       int64
		protectedDropped bool // future: surface if anyone needs it
	)
	_ = protectedDropped // currently unused; reserved for symmetry

	for i, m := range manifests {
		if pattern, hit := matchesProtected(m.tags, patterns, candidate.GetProtectedTagPatterns()); hit {
			if len(protectedOut) < maxProtectedResults {
				protectedOut = append(protectedOut, EvaluationProtected{
					ManifestID:     m.manifestID,
					ManifestDigest: m.digest,
					Tags:           m.tags,
					MatchedPattern: pattern,
				})
			}
			continue
		}

		// Per-rule pass. Collect every kind that selects this manifest so the
		// UI sees ALL reasons (not just the first one) — the spec explicitly
		// requires this for operator clarity.
		var reasons []string
		if rule, ok := rulesByKind[ruleKindMaxAgeDays]; ok {
			cutoff := now.Add(-time.Duration(rule.GetValue()) * 24 * time.Hour)
			if m.createdAt.Before(cutoff) {
				reasons = append(reasons, ruleKindMaxAgeDays)
			}
		}
		if _, ok := rulesByKind[ruleKindMaxCount]; ok {
			if _, selected := maxCountSelected[i]; selected {
				reasons = append(reasons, ruleKindMaxCount)
			}
		}
		if _, ok := rulesByKind[ruleKindMaxSizeBytes]; ok {
			if _, selected := maxSizeSelected[i]; selected {
				reasons = append(reasons, ruleKindMaxSizeBytes)
			}
		}
		if rule, ok := rulesByKind[ruleKindDanglingGraceDays]; ok {
			// Schema gap: we have no tag-removal time, so this collapses to
			// "no tags currently AND created_at older than N days". Doc'd
			// inline + in the file-level comment.
			if len(m.tags) == 0 {
				cutoff := now.Add(-time.Duration(rule.GetValue()) * 24 * time.Hour)
				if m.createdAt.Before(cutoff) {
					reasons = append(reasons, ruleKindDanglingGraceDays)
				}
			}
		}
		// max_idle_days (FE-API-043): "delete manifests that haven't been
		// pulled in N days." The naive predicate (last_pulled_at < cutoff OR
		// last_pulled_at IS NULL) is a foot-gun — a brand-new push with NULL
		// last_pulled_at would match immediately because pull tracking only
		// started after FE-API-042. We gate on created_at AS WELL so a
		// manifest is only "idle" when it has had a real chance to be
		// pulled: it must be at least N days old, AND there must be no
		// pull within the last N days. NULL last_pulled_at on a manifest
		// that's at least N days old IS idle — that's the intended outcome
		// (manifests that existed pre-pull-tracking but were never touched).
		if rule, ok := rulesByKind[ruleKindMaxIdleDays]; ok {
			cutoff := now.Add(-time.Duration(rule.GetValue()) * 24 * time.Hour)
			// Combined gate: created_at < cutoff (manifest old enough to have
			// been pulled) AND (last_pulled_at IS NULL OR last_pulled_at <
			// cutoff). Strict "<" matches max_age_days boundary semantics —
			// at exactly N days we keep, only strictly older than N days
			// counts as idle. Documented at the file head.
			if m.createdAt.Before(cutoff) {
				if m.lastPulledAt == nil || m.lastPulledAt.Before(cutoff) {
					reasons = append(reasons, ruleKindMaxIdleDays)
				}
			}
		}

		if len(reasons) == 0 {
			continue
		}
		// Sort reasons ascending so the wire output is stable across runs.
		sort.Strings(reasons)
		fullDelete = append(fullDelete, EvaluationCandidate{
			ManifestID:     m.manifestID,
			ManifestDigest: m.digest,
			Tags:           m.tags,
			PushedAt:       m.createdAt,
			SizeBytes:      m.sizeBytes,
			Reasons:        reasons,
		})
		totalCount++
		totalBytes += m.sizeBytes
	}

	// Stability: sort by (created_at ASC, manifest_digest ASC) so the UI
	// renders oldest-first deterministically. The SQL gave us DESC ordering
	// which is what max_count needs; the wire output flips it back.
	sort.SliceStable(fullDelete, func(i, j int) bool {
		if !fullDelete[i].PushedAt.Equal(fullDelete[j].PushedAt) {
			return fullDelete[i].PushedAt.Before(fullDelete[j].PushedAt)
		}
		return fullDelete[i].ManifestDigest < fullDelete[j].ManifestDigest
	})

	// Truncation. Always compute totals from the FULL set above, then cap.
	truncated := false
	if len(fullDelete) > maxDeleteResults {
		fullDelete = fullDelete[:maxDeleteResults]
		truncated = true
	}

	return &EvaluationResult{
		WouldDelete:      fullDelete,
		ProtectedSkipped: protectedOut,
		TotalCount:       totalCount,
		TotalBytes:       totalBytes,
		EvaluatedAt:      now,
		Truncated:        truncated,
	}, nil
}

// loadManifestsForEval pulls every manifest in the repo, joined with its tag
// names, ordered by created_at DESC. The DESC ordering is required by the
// max_count / max_size_bytes rules — "keep the newest N" needs the working
// set sorted newest-first so the per-rule helpers can do a single linear
// pass instead of an O(N log N) re-sort.
//
// tags is COALESCED to an empty array (not NULL) so the Go decode is
// uniform across "tagged manifest" and "dangling manifest" rows.
//
// Tenant isolation: the manifests table is FK-attached to repositories,
// which is itself filtered by tenant_id in the application layer; we still
// constrain on m.tenant_id directly here so a misuse of repoID across
// tenants cannot leak rows.
func (r *Repository) loadManifestsForEval(ctx context.Context, tenantID, repoID string) ([]evalManifest, error) {
	// last_pulled_at is selected as a nullable timestamp — FE-API-043's
	// max_idle_days rule treats NULL distinctly from "an old timestamp", so
	// the scan decodes it through a *time.Time on evalManifest below.
	const q = `
		SELECT m.id::text,
		       m.digest,
		       m.image_size_bytes,
		       m.created_at,
		       m.last_pulled_at,
		       COALESCE(array_agg(t.name ORDER BY t.name) FILTER (WHERE t.name IS NOT NULL), '{}'::text[]) AS tag_names
		FROM   manifests m
		LEFT JOIN tags t
		       ON t.repo_id = m.repo_id
		      AND t.manifest_digest = m.digest
		WHERE  m.repo_id = $1
		  AND  m.tenant_id = $2
		GROUP BY m.id, m.digest, m.image_size_bytes, m.created_at, m.last_pulled_at
		ORDER BY m.created_at DESC, m.digest ASC`

	// Route to the read replica when available — the evaluator is a
	// read-heavy, repo-scoped scan that fits the same routing rule as
	// ListRepositories / ListTags.
	rows, err := r.reader().Query(ctx, q, repoID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query manifests for eval: %w", err)
	}
	defer rows.Close()

	out := make([]evalManifest, 0, 256)
	for rows.Next() {
		var m evalManifest
		// last_pulled_at is nullable on disk — scan into a *time.Time so the
		// max_idle_days rule can distinguish "never pulled" (nil) from
		// "pulled long ago" (non-nil, older than threshold).
		if err := rows.Scan(&m.manifestID, &m.digest, &m.sizeBytes, &m.createdAt, &m.lastPulledAt, &m.tags); err != nil {
			return nil, fmt.Errorf("scan eval manifest: %w", err)
		}
		// COALESCE(... '{}'::text[]) produces a non-nil slice; nil-guard
		// defensively in case a future driver upgrade returns nil for
		// empty arrays.
		if m.tags == nil {
			m.tags = []string{}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eval manifests: %w", err)
	}
	return out, nil
}

// indexRules turns the rule slice into a kind→rule lookup. The handler
// already rejected duplicate kinds at the write seam, but the upsert path
// also rejects duplicates so the in-flight candidate from the dry-run
// endpoint has the same shape. Keeping this defensive (last write wins)
// means a corrupt persisted policy still evaluates rather than blowing up.
func indexRules(rules []*metadatav1.RetentionRule) map[string]*metadatav1.RetentionRule {
	out := make(map[string]*metadatav1.RetentionRule, len(rules))
	for _, r := range rules {
		if r == nil {
			continue
		}
		out[r.GetKind()] = r
	}
	return out
}

// compileProtectedPatterns compiles every pattern and returns a slice of
// regex pointers aligned with the input slice. A nil rule is treated as
// "no patterns" — used when the candidate has empty protected_tag_patterns.
//
// We use regexp.Compile (not MustCompile) so a malformed pattern surfaces
// as an error instead of a panic — the handler validates patterns at the
// write seam, but a corrupted DB row should still produce a clean error
// rather than crash the gRPC process.
func compileProtectedPatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile protected pattern %d: %w", i, err)
		}
		out[i] = re
	}
	return out, nil
}

// matchesProtected returns the first protected pattern matched by any tag
// on the manifest, plus a hit flag. Returns ("", false) when no tag matches
// any pattern (or when the manifest has no tags — dangling manifests can't
// be protected, by design).
//
// `rawPatterns` is the original string slice; we return the source string
// so the wire response is exact text the operator wrote (round-trippable).
func matchesProtected(tags []string, compiled []*regexp.Regexp, rawPatterns []string) (string, bool) {
	if len(tags) == 0 || len(compiled) == 0 {
		return "", false
	}
	for _, tag := range tags {
		for i, re := range compiled {
			if re == nil {
				continue
			}
			if re.MatchString(tag) {
				return rawPatterns[i], true
			}
		}
	}
	return "", false
}

// buildMaxCountSet returns the set of manifest indices (within the
// sorted-DESC `manifests` slice) selected by a max_count rule. A nil rule
// (kind not present in the candidate) returns an empty set so the per-rule
// pass collapses to a cheap map miss.
//
// Selection semantics: indices [N..) are selected — the newest N are kept.
// This is computed against ALL non-protected manifests by the caller's
// classification loop; this helper does NOT pre-filter for protected
// status. The composition is intentional — see the file-level comment.
func buildMaxCountSet(manifests []evalManifest, rule *metadatav1.RetentionRule) map[int]struct{} {
	if rule == nil {
		return nil
	}
	keep := int(rule.GetValue())
	if keep < 0 {
		keep = 0
	}
	if keep >= len(manifests) {
		return nil
	}
	out := make(map[int]struct{}, len(manifests)-keep)
	for i := keep; i < len(manifests); i++ {
		out[i] = struct{}{}
	}
	return out
}

// buildMaxSizeSet returns indices selected by a max_size_bytes rule.
// Walks the sorted-DESC slice summing size_bytes; every manifest AFTER
// the running sum exceeds the cap is a candidate. Indices i where
// runningSum > cap before we add manifests[i] are selected.
//
// Boundary: when the running sum reaches the cap EXACTLY at index k,
// manifest k is the last one kept; k+1 is the first selected. This
// matches operator intuition ("keep until we hit the cap, evict
// everything older").
func buildMaxSizeSet(manifests []evalManifest, rule *metadatav1.RetentionRule) map[int]struct{} {
	if rule == nil {
		return nil
	}
	cap := rule.GetValue()
	if cap <= 0 {
		return nil
	}
	out := make(map[int]struct{}, 8)
	var running int64
	for i, m := range manifests {
		if running >= cap {
			out[i] = struct{}{}
			continue
		}
		running += m.sizeBytes
	}
	return out
}
