// Package service — oidc_subject.go is the pure glob matcher + syntax
// validator that gates which OIDC `sub` claims a trust config will accept.
//
// SECURITY-CRITICAL: a wrong implementation here lets a CI runner mint
// tokens for the wrong service account. The `/` semantics are the load-
// bearing distinction:
//
//   - `*`  matches zero or more characters EXCLUDING `/`.
//   - `**` matches zero or more characters INCLUDING `/`.
//   - `?`  matches exactly one character EXCLUDING `/`.
//
// The pattern is anchored at both ends — the entire `sub` must consume the
// entire pattern. This mirrors the GitHub Actions / GitLab CI / Buildkite
// docs on OIDC subject filtering, which themselves anchor their glob.
//
// Implementation note: recursive descent over regex. The recursive matcher
// is easier to reason about for `**` semantics (regex `[^/]*` vs `.*` is a
// trap), and the worst-case time for realistic CI subjects (< 200 chars,
// < 5 wildcards) is sub-microsecond.
package service

import (
	"errors"
	"fmt"
	"strings"
)

// subjectMatches reports whether `subject` matches the glob `pattern`.
// See the package doc comment for supported metacharacters and their `/`
// semantics. Anchored at both ends.
func subjectMatches(pattern, subject string) bool {
	return matchGlob(pattern, subject)
}

// matchGlob is the recursive matcher. Returns true iff `pat` (with glob
// metacharacters) matches `s` entirely. Iterative for the literal-byte
// path (the common case) and recursive-with-backtracking for wildcards.
func matchGlob(pat, s string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case '*':
			// `**` matches any run of chars including `/`.
			// `*`  matches any run of chars excluding `/`.
			doublestar := len(pat) >= 2 && pat[1] == '*'
			if doublestar {
				rest := pat[2:]
				// Try matching the rest at every position from 0 to len(s).
				// Crossing `/` is allowed.
				for i := 0; i <= len(s); i++ {
					if matchGlob(rest, s[i:]) {
						return true
					}
				}
				return false
			}
			rest := pat[1:]
			// Single `*` — try matching the rest at every position, but
			// bail as soon as we'd cross a `/` (which is forbidden for
			// single-star).
			for i := 0; i <= len(s); i++ {
				if matchGlob(rest, s[i:]) {
					return true
				}
				// After this iteration we will advance past s[i]. If that
				// byte is `/`, the next iteration would have crossed the
				// slash inside the wildcard span — bail.
				if i < len(s) && s[i] == '/' {
					return false
				}
			}
			return false
		case '?':
			// Single-char wildcard, excluding `/`.
			if len(s) == 0 || s[0] == '/' {
				return false
			}
			pat = pat[1:]
			s = s[1:]
		default:
			// Literal byte match.
			if len(s) == 0 || s[0] != pat[0] {
				return false
			}
			pat = pat[1:]
			s = s[1:]
		}
	}
	// Pattern exhausted — match iff subject is also exhausted (anchored
	// at the right edge).
	return len(s) == 0
}

// validateGlobSyntax returns nil if `pattern` is a syntactically valid
// subject glob, or a descriptive error otherwise. Used at trust-create
// time so a misformed pattern is rejected before it reaches the DB.
//
// Rejection rules:
//   - The pattern must be non-empty. An empty subject_pattern would match
//     only the empty `sub` claim, which is never legitimate (every CI IdP
//     emits a non-empty subject).
//   - No more than two consecutive `*` characters (`***` is ambiguous —
//     would the third star be a literal or part of the wildcard?). Two
//     stars is the well-defined `**` doublestar.
func validateGlobSyntax(pattern string) error {
	if pattern == "" {
		return errors.New("pattern must be non-empty")
	}
	// Walk the pattern looking for runs of '*' longer than 2. We do not
	// reject ?, [], or other characters — those are either supported or
	// treated as literals (subjectMatches has no special handling for
	// brackets so they match literally, which is the safe default).
	for i := 0; i < len(pattern); {
		if pattern[i] != '*' {
			i++
			continue
		}
		runLen := 0
		for j := i; j < len(pattern) && pattern[j] == '*'; j++ {
			runLen++
		}
		if runLen > 2 {
			return fmt.Errorf("pattern contains a run of %d consecutive '*' at offset %d (max 2 for `**`)", runLen, i)
		}
		i += runLen
	}
	// Reject leading/trailing whitespace — almost always a copy-paste bug.
	if pattern != strings.TrimSpace(pattern) {
		return errors.New("pattern must not have leading or trailing whitespace")
	}
	return nil
}
