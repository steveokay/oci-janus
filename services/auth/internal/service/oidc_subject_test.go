// Package service — oidc_subject_test.go covers the subject-glob matcher
// + the syntax validator. These tests are the security gate for FUT-001
// federated workload identity — a wrong implementation lets a CI runner
// mint tokens for the wrong service account, so the listed cases are
// non-negotiable. Do not loosen them without coordinating with the
// security-review process.

package service

import "testing"

// TestSubjectMatches enforces the `/` semantics that separate `*` (single
// segment) from `**` (doublestar, crosses `/`). Examples mirror the real
// subject shapes GitHub Actions / GitLab CI emit so the tests double as
// living documentation.
func TestSubjectMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"literal exact", "repo:steveokay/oci-janus:ref:refs/heads/main", "repo:steveokay/oci-janus:ref:refs/heads/main", true},
		{"literal mismatch", "repo:steveokay/oci-janus:ref:refs/heads/main", "repo:steveokay/oci-janus:ref:refs/heads/dev", false},
		{"star matches single segment", "repo:steveokay/oci-janus:ref:refs/heads/*", "repo:steveokay/oci-janus:ref:refs/heads/main", true},
		{"star does NOT cross slash", "repo:steveokay/oci-janus:ref:refs/heads/*", "repo:steveokay/oci-janus:ref:refs/heads/feat/x", false},
		{"doublestar crosses slash", "repo:steveokay/oci-janus:ref:refs/heads/**", "repo:steveokay/oci-janus:ref:refs/heads/feat/x", true},
		{"question matches single char", "repo:org/r:env:prod-?", "repo:org/r:env:prod-1", true},
		{"question does NOT cross slash", "repo:org/r:env:prod-?", "repo:org/r:env:prod-/", false},
		{"empty pattern rejects non-empty", "", "anything", false},
		{"empty pattern accepts empty", "", "", true},
		{"prefix wildcard blocked by slash", "*:ref:refs/heads/main", "repo:org/r:ref:refs/heads/main", false},
		{"suffix wildcard match", "repo:org/r:*", "repo:org/r:env-x", true},
		{"no anchor needed (pattern is whole subject)", "repo:a", "repo:a", true},
		{"partial match must be rejected", "repo:a", "repo:abc", false},
		// Extra security-relevant cases:
		// - Anchor enforcement: pattern must consume the WHOLE subject.
		{"subject longer than pattern", "repo:a", "repo:abc", false},
		// - Doublestar at end consumes any tail including empty.
		{"doublestar at end matches empty tail", "repo:steveokay/oci-janus:**", "repo:steveokay/oci-janus:", true},
		// - Doublestar in middle.
		{"doublestar in middle", "repo:**:ref", "repo:steveokay/oci-janus/x:ref", true},
		// - Star matches empty.
		{"star matches empty", "abc*def", "abcdef", true},
		// - Star with content.
		{"star with content", "abc*def", "abcXYZdef", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := subjectMatches(tc.pattern, tc.subject)
			if got != tc.want {
				t.Errorf("subjectMatches(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
			}
		})
	}
}

// TestValidateGlobSyntax covers the syntax-level rejections used at
// trust-create time so a misformed pattern is rejected before it lands in
// the DB.
func TestValidateGlobSyntax(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"empty rejected", "", true},
		{"single literal accepted", "repo:a", false},
		{"single star accepted", "repo:*", false},
		{"doublestar accepted", "repo:**", false},
		{"triple star rejected", "repo:***", true},
		{"quadruple star rejected", "repo:****/foo", true},
		{"leading whitespace rejected", " repo:a", true},
		{"trailing whitespace rejected", "repo:a ", true},
		{"realistic GH actions pattern", "repo:steveokay/oci-janus:ref:refs/heads/*", false},
		{"realistic GH actions doublestar", "repo:steveokay/oci-janus:**", false},
		{"realistic GitLab pattern", "project_path:group/project:ref_type:branch:ref:main", false},
		// Two separate runs of 2 stars are fine — only consecutive runs > 2 are rejected.
		{"two separate doublestars accepted", "repo:**/x:**", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGlobSyntax(tc.pattern)
			if tc.wantErr && err == nil {
				t.Errorf("validateGlobSyntax(%q) = nil, want error", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateGlobSyntax(%q) = %v, want nil", tc.pattern, err)
			}
		})
	}
}
