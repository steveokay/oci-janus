// Package handler — shared retention policy validation.
//
// Validation rules live here (not in retention.go) so the per-repo
// (FE-API-037) and per-org-default (FE-API-039) handlers both consume the
// same allowlist + value caps + pattern checks. The risk of having two
// copies drift is the actual concern — there is no enforcement seam in the
// SQL layer past the JSONB column, so any rule the API accepts gets
// persisted as-is.
//
// Authoritative rules:
//
//   - When enabled = true, rules[] must be non-empty (an "enabled but empty"
//     policy is a silent no-op — operator surprise).
//   - Each rule's `kind` must be in validRetentionRuleKinds.
//   - Each rule's `value` must be > 0 and ≤ the per-kind cap in
//     retentionMaxValues.
//   - At most one rule per kind.
//   - Each protected_tag_pattern is ≤ maxProtectedTagPatternLen chars and
//     compiles as a Go regexp.
//
// All errors return codes.InvalidArgument; the BFF maps that to HTTP 400.
//
// Note: max_idle_days is in the allowlist even though the executor
// (FE-API-040) doesn't honor it yet — FE-API-043 flips it on without an API
// or schema change. The org-default path inherits the same forward-compat.
package handler

import (
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// validRetentionRuleKinds is the closed allowlist accepted by both the
// per-repo Upsert and the per-org-default Upsert. Mirrors the
// retention_rule_kind enum on the metadata DB.
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
// nonsensical value. Generous numbers — 100 years for day-based rules, 10M
// manifests for max_count, 100 TiB for max_size_bytes — but they prevent
// overflow surprises in the executor and clamp pathological inputs at the
// API boundary.
var retentionMaxValues = map[string]int64{
	"max_age_days":        36500,                           // 100 years
	"max_count":           10_000_000,                      // 10M manifests
	"max_size_bytes":      100 * 1024 * 1024 * 1024 * 1024, // 100 TiB
	"dangling_grace_days": 365,
	"max_idle_days":       36500, // 100 years
}

// maxProtectedTagPatternLen caps each protected_tag_pattern string so a
// large regex (which the executor will compile per-rule) cannot blow up
// memory.
const maxProtectedTagPatternLen = 256

// validateRetentionRules enforces the per-call invariants on the rule list.
// Shared between per-repo (FE-API-037) and per-org-default (FE-API-039)
// upserts + the dry-run path. See package doc for the rule set.
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

// validateProtectedTagPatterns enforces the per-pattern invariants:
//   - max maxProtectedTagPatternLen chars (defends against pathological
//     regex memory cost).
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
