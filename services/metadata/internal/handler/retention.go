// Package handler — FE-API-037 per-repo retention policy CRUD.
//
// Validation rules enforced before any DB access:
//
//   - When enabled=true, rules[] must be non-empty.
//   - Each rule's `kind` must be one of the allowlist (max_age_days,
//     max_count, max_size_bytes, dangling_grace_days, max_idle_days).
//     max_idle_days is accepted at the API level even though the executor
//     (FE-API-040) does not honor it yet — the schema and proto are
//     deliberately ready for FE-API-043 to switch it on without a migration.
//   - Each rule's `value` must be > 0 and within a sane per-kind upper bound.
//   - At most one rule per kind — duplicate kinds reject.
//   - Each protected_tag_pattern is ≤ 256 chars and compiles as a Go regexp.
//
// All errors surface as codes.InvalidArgument so the management BFF maps to
// HTTP 400. The repository's ErrNotFound surfaces as codes.NotFound for the
// repo-not-found case (FK violation) and for missing rows on Delete.
package handler

import (
	"context"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// validRetentionRuleKinds is the closed allowlist accepted by Upsert. It
// mirrors the retention_rule_kind enum on the metadata DB. Keeping it as a
// map (not a slice) is a single hash lookup per rule instead of a linear
// scan; the set is tiny so memory cost is negligible.
var validRetentionRuleKinds = map[string]bool{
	"max_age_days":        true,
	"max_count":           true,
	"max_size_bytes":      true,
	"dangling_grace_days": true,
	// max_idle_days is accepted now even though the executor (FE-API-040)
	// ignores it. FE-API-043 will switch enforcement on without any schema
	// or API change.
	"max_idle_days": true,
}

// retentionMaxValues caps each kind so a stray UI input can't persist a
// nonsensical value. The numbers are generous — 100 years on day-based
// rules, 10M manifests for max_count, 100 TiB for max_size_bytes — but
// they prevent overflow surprises in the executor and clamp pathological
// inputs at the API boundary.
var retentionMaxValues = map[string]int64{
	"max_age_days":        36500,                  // 100 years
	"max_count":           10_000_000,             // 10M manifests
	"max_size_bytes":      100 * 1024 * 1024 * 1024 * 1024, // 100 TiB
	"dangling_grace_days": 365,
	"max_idle_days":       36500, // 100 years
}

// maxProtectedTagPatternLen caps each protected_tag_pattern string so a
// large regex (which the executor will compile per-rule) cannot blow up
// memory.
const maxProtectedTagPatternLen = 256

// GetRepoRetentionPolicy returns the policy attached to a repository. NotFound
// when no row exists; the BFF maps NotFound to "no per-repo policy" so the
// FE-API-039 org-default fallback can kick in.
func (h *MetadataHandler) GetRepoRetentionPolicy(ctx context.Context, req *metadatav1.GetRepoRetentionPolicyRequest) (*metadatav1.RetentionPolicy, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	policy, err := h.repo.GetRepoRetentionPolicy(ctx, req.GetTenantId(), req.GetRepoId())
	if err != nil {
		return nil, mapErr(err)
	}
	return policy, nil
}

// UpsertRepoRetentionPolicy validates the request and writes through to the
// repository. preview_until is owned server-side — see retention.go in the
// repository layer for the rules around when it gets reset.
func (h *MetadataHandler) UpsertRepoRetentionPolicy(ctx context.Context, req *metadatav1.UpsertRepoRetentionPolicyRequest) (*metadatav1.RetentionPolicy, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	if err := validateRetentionRules(req.GetEnabled(), req.GetRules()); err != nil {
		return nil, err
	}
	if err := validateProtectedTagPatterns(req.GetProtectedTagPatterns()); err != nil {
		return nil, err
	}

	policy, err := h.repo.UpsertRepoRetentionPolicy(
		ctx,
		req.GetTenantId(),
		req.GetRepoId(),
		req.GetEnabled(),
		req.GetRules(),
		req.GetProtectedTagPatterns(),
		req.GetUpdatedBy(),
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return policy, nil
}

// DeleteRepoRetentionPolicy removes the per-repo override. NotFound when no
// row exists so the BFF surfaces a 404; the BFF interprets that as "nothing
// to do" rather than an inheritance reset (the FE-API-039 fallback always
// applies when no row exists).
func (h *MetadataHandler) DeleteRepoRetentionPolicy(ctx context.Context, req *metadatav1.DeleteRepoRetentionPolicyRequest) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	if err := h.repo.DeleteRepoRetentionPolicy(ctx, req.GetTenantId(), req.GetRepoId()); err != nil {
		return nil, mapErr(err)
	}
	return &emptypb.Empty{}, nil
}

// validateRetentionRules enforces the per-call invariants on the rule list:
//   - When enabled, the rule list must be non-empty (a policy with no rules
//     would be a silent no-op enforcement — operator surprise).
//   - Each kind must be in the allowlist, each value > 0, each value below
//     its kind's cap.
//   - Each kind appears at most once.
func validateRetentionRules(enabled bool, rules []*metadatav1.RetentionRule) error {
	if enabled && len(rules) == 0 {
		return status.Error(codes.InvalidArgument, "rules must be non-empty when enabled=true")
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		kind := rule.GetKind()
		if !validRetentionRuleKinds[kind] {
			// Don't echo the kind value back — the response body is a
			// candidate for log injection if the caller is hostile and the
			// message is rendered without escaping. The frontend already
			// renders the allowlist client-side.
			return status.Error(codes.InvalidArgument, "unknown retention rule kind")
		}
		if seen[kind] {
			return status.Error(codes.InvalidArgument, "duplicate retention rule kind")
		}
		seen[kind] = true
		if rule.GetValue() <= 0 {
			return status.Error(codes.InvalidArgument, "retention rule value must be > 0")
		}
		if cap, ok := retentionMaxValues[kind]; ok && rule.GetValue() > cap {
			return status.Error(codes.InvalidArgument, "retention rule value exceeds maximum for kind")
		}
	}
	return nil
}

// ─── FE-API-038: dry-run evaluator ─────────────────────────────────────────

// EvaluateRetention validates the candidate policy and forwards the
// evaluation to the repository. The handler:
//
//   - Reuses validateRetentionRules / validateProtectedTagPatterns so the
//     authoritative validation rules live in one place (also exercised by
//     the Upsert tests, so we don't have two copies that can drift apart).
//   - Clamps max_delete_results to [1, 5000] and max_protected_results to
//     [1, 500]. A hostile client cannot trick the evaluator into
//     materialising a huge response via inflated caps — the clamps land
//     before the repository call.
//   - Maps an empty repo (no manifests at all) to an empty would_delete
//     response with total_count=0, NOT NotFound. The caller (management
//     BFF) already resolved the repo_id via GetRepositoryByName; if THAT
//     fails it surfaces NotFound. Treating empty-repo as NotFound would
//     hide the legitimate "I just created an empty repo and want to dry-run
//     a future policy" case.
//
// Tenant isolation: tenant_id is required and forwarded into the repository
// SQL, where the manifests table is filtered on tenant_id directly.
func (h *MetadataHandler) EvaluateRetention(ctx context.Context, req *metadatav1.EvaluateRetentionRequest) (*metadatav1.EvaluateRetentionResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	cand := req.GetCandidate()
	if cand == nil {
		return nil, status.Error(codes.InvalidArgument, "candidate is required")
	}
	// Same validation as the Upsert path. We pass cand.GetEnabled() through
	// so the "enabled + empty rules" case (silent no-op policy) is rejected
	// on the dry-run path too. The UI relies on this so it can render a
	// clean error before the operator commits to a useless policy.
	if err := validateRetentionRules(cand.GetEnabled(), cand.GetRules()); err != nil {
		return nil, err
	}
	if err := validateProtectedTagPatterns(cand.GetProtectedTagPatterns()); err != nil {
		return nil, err
	}

	maxDelete := clampInt(int(req.GetMaxDeleteResults()), repository.DefaultMaxDeleteResults, 1, repository.MaxMaxDeleteResults)
	maxProtected := clampInt(int(req.GetMaxProtectedResults()), repository.DefaultMaxProtectedResults, 1, repository.MaxMaxProtectedResults)

	result, err := h.repo.EvaluateRetention(ctx, req.GetTenantId(), req.GetRepoId(), cand, maxDelete, maxProtected)
	if err != nil {
		return nil, mapErr(err)
	}

	// Convert the in-Go EvaluationResult to the wire shape. Allocate
	// non-nil slices so the wire response is always JSON arrays — keeps
	// the BFF + dashboard JSON parsing trivial.
	wd := make([]*metadatav1.RetentionDeletionCandidate, 0, len(result.WouldDelete))
	for _, c := range result.WouldDelete {
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		reasons := c.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		wd = append(wd, &metadatav1.RetentionDeletionCandidate{
			ManifestId:     c.ManifestID,
			ManifestDigest: c.ManifestDigest,
			Tags:           tags,
			PushedAt:       timestamppb.New(c.PushedAt),
			SizeBytes:      c.SizeBytes,
			Reasons:        reasons,
		})
	}
	ps := make([]*metadatav1.RetentionProtectedManifest, 0, len(result.ProtectedSkipped))
	for _, p := range result.ProtectedSkipped {
		tags := p.Tags
		if tags == nil {
			tags = []string{}
		}
		ps = append(ps, &metadatav1.RetentionProtectedManifest{
			ManifestId:     p.ManifestID,
			ManifestDigest: p.ManifestDigest,
			Tags:           tags,
			MatchedPattern: p.MatchedPattern,
		})
	}
	return &metadatav1.EvaluateRetentionResponse{
		WouldDelete:      wd,
		ProtectedSkipped: ps,
		TotalCount:       result.TotalCount,
		TotalBytes:       result.TotalBytes,
		EvaluatedAt:      timestamppb.New(result.EvaluatedAt),
		Truncated:        result.Truncated,
	}, nil
}

// clampInt returns `v` constrained to [min, max], substituting `def` when
// v ≤ 0 (the proto zero-value path: caller didn't set the field). Pure
// helper kept in this file so the handler is self-contained.
func clampInt(v, def, min, max int) int {
	if v <= 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// validateProtectedTagPatterns enforces the per-pattern invariants:
//   - max 256 chars (defends against pathological regex memory cost).
//   - must compile with Go's regexp package — protected_tag_patterns is
//     consumed at executor time as a Go regexp, so a malformed pattern would
//     either crash the executor or silently match nothing. We reject at the
//     write seam so the operator sees the error immediately.
func validateProtectedTagPatterns(patterns []string) error {
	for _, p := range patterns {
		if len(p) > maxProtectedTagPatternLen {
			return status.Error(codes.InvalidArgument, "protected_tag_pattern exceeds 256 characters")
		}
		if _, err := regexp.Compile(p); err != nil {
			return status.Error(codes.InvalidArgument, "protected_tag_pattern is not a valid regex")
		}
	}
	return nil
}
