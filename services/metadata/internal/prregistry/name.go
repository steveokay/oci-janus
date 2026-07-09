package prregistry

// name.go — namespace-name derivation (FUT-023 §7.3).
//
// A per-PR org is named "pr-<sanitized-repo>-<N>". The whole name must
// satisfy the platform org-name regex ^[a-z0-9-]{2,64}$ (CLAUDE.md §7), so we
// sanitize the repo portion and, when the assembled name would exceed 64
// chars, truncate ONLY the middle (repo) segment — the "pr-" prefix and the
// "-<N>" suffix are load-bearing (they namespace the org + tie it back to the
// PR) and are always kept intact.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// orgNamePrefix is the fixed marker that stamps an org as a PR-scoped
// namespace. Kept intact through truncation.
const orgNamePrefix = "pr-"

// maxOrgNameLen is the upper bound from the org-name regex ^[a-z0-9-]{2,64}$.
const maxOrgNameLen = 64

// orgNameRE mirrors the platform org-name allowlist (CLAUDE.md §7). Duplicated
// here rather than imported because the only existing Go source lives in
// services/management/internal/handler (a different module) — the regex string
// is the shared contract, not the compiled var.
var orgNameRE = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)

// multiDashRE collapses runs of '-' produced by sanitising adjacent
// out-of-class characters into a single '-'.
var multiDashRE = regexp.MustCompile(`-+`)

// nonOrgCharRE matches any character outside the org allowlist [a-z0-9-].
// Applied after lowercasing so uppercase letters are handled by the
// lowercase step, not replaced with '-'.
var nonOrgCharRE = regexp.MustCompile(`[^a-z0-9-]`)

// sanitize lowercases s, replaces every character outside [a-z0-9-] with '-',
// collapses repeated '-' into one, and trims leading/trailing '-'. The result
// contains only [a-z0-9-] with no leading/trailing/duplicate dashes (possibly
// empty when s had no usable characters).
func sanitize(s string) string {
	s = strings.ToLower(s)
	s = nonOrgCharRE.ReplaceAllString(s, "-")
	s = multiDashRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// deriveOrgName builds the per-PR org name "pr-<sanitized-repo>-<N>" and
// guarantees it satisfies ^[a-z0-9-]{2,64}$.
//
// When the full name would exceed 64 chars, only the sanitized repo segment
// is truncated (the "pr-" prefix and "-<N>" suffix are preserved) — and any
// trailing '-' left by the cut is trimmed so the name never ends in a dash.
// Returns an error when a valid name can't be produced (empty repo after
// sanitising, or the fixed prefix+suffix alone already overflows the bound);
// the caller logs it and maps to Ignored — never a 500.
func deriveOrgName(repoName string, prNumber int) (string, error) {
	repo := sanitize(repoName)
	if repo == "" {
		return "", fmt.Errorf("prregistry: repo name %q sanitizes to empty", repoName)
	}

	suffix := "-" + strconv.Itoa(prNumber)
	// The budget for the middle segment is what's left after the fixed
	// prefix + suffix. If that's non-positive the number alone is too long
	// to leave room for any repo segment ⇒ can't build a valid name.
	budget := maxOrgNameLen - len(orgNamePrefix) - len(suffix)
	if budget <= 0 {
		return "", fmt.Errorf("prregistry: pr #%d leaves no room for repo segment", prNumber)
	}

	if len(repo) > budget {
		// Truncate the middle segment, then re-trim a trailing '-' the cut
		// may have exposed so the assembled name stays regex-valid.
		repo = strings.TrimRight(repo[:budget], "-")
		if repo == "" {
			return "", fmt.Errorf("prregistry: repo name %q truncates to empty", repoName)
		}
	}

	name := orgNamePrefix + repo + suffix
	if !orgNameRE.MatchString(name) {
		// Defensive: sanitize + budget math should already guarantee this,
		// but never emit a name that would fail the handler's own validation.
		return "", fmt.Errorf("prregistry: derived name %q fails org regex", name)
	}
	return name, nil
}
