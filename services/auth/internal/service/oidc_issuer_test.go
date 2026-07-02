// Package service — oidc_issuer_test.go covers the prefix-match issuer
// allowlist + the CSV parser.

package service

import (
	"reflect"
	"testing"
)

// TestIssuerAllowed walks through realistic issuer-URL shapes against
// allowlist entries, including the fail-closed empty-allowlist default
// and the prefix-match semantics.
func TestIssuerAllowed(t *testing.T) {
	cases := []struct {
		name   string
		allow  []string
		issuer string
		want   bool
	}{
		{"empty allowlist rejects everything", nil, "https://token.actions.githubusercontent.com", false},
		{"exact match", []string{"https://token.actions.githubusercontent.com"}, "https://token.actions.githubusercontent.com", true},
		{"prefix match — trailing slash on issuer", []string{"https://token.actions.githubusercontent.com"}, "https://token.actions.githubusercontent.com/", true},
		{"path-suffix is allowed by prefix", []string{"https://gitlab.com"}, "https://gitlab.com/group", true},
		{"different scheme rejected", []string{"https://token.actions.githubusercontent.com"}, "http://token.actions.githubusercontent.com", false},
		{"different host rejected", []string{"https://token.actions.githubusercontent.com"}, "https://attacker.example.com", false},
		{"trailing-slash allowlist does NOT match no-slash issuer", []string{"https://gitlab.com/"}, "https://gitlab.com", false},
		{"any of multiple", []string{"https://a.example", "https://b.example"}, "https://b.example/foo", true},
		{"empty issuer never allowed", []string{"https://token.actions.githubusercontent.com"}, "", false},
		{"empty allowlist entry must not match everything", []string{""}, "https://token.actions.githubusercontent.com", false},

		// SEC-057 regression tests — bare HasPrefix let an attacker
		// register an issuer of `<allowed-prefix>.evil.com` and pass
		// the allowlist gate. Fixed by requiring the character after
		// the matched prefix to be `/` or end-of-string.
		{"SEC-057: suffix-extended hostname rejected",
			[]string{"https://token.actions.githubusercontent.com"},
			"https://token.actions.githubusercontent.com.evil.com", false},
		{"SEC-057: dashed suffix rejected",
			[]string{"https://token.actions.githubusercontent.com"},
			"https://token.actions.githubusercontent.com-evil.com", false},
		{"SEC-057: subdomain lookalike rejected",
			[]string{"https://gitlab.com"},
			"https://gitlab.com.attacker.example", false},
		{"SEC-057: legit path suffix still allowed (URL-path boundary)",
			[]string{"https://gitlab.com"},
			"https://gitlab.com/group/project", true},
		{"SEC-057: exact match still allowed (end-of-string boundary)",
			[]string{"https://gitlab.com"}, "https://gitlab.com", true},
		{"SEC-057: trailing-slash allowlist matches deeper path",
			[]string{"https://gitlab.com/"}, "https://gitlab.com/group", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issuerAllowed(tc.allow, tc.issuer)
			if got != tc.want {
				t.Errorf("issuerAllowed(%v, %q) = %v, want %v", tc.allow, tc.issuer, got, tc.want)
			}
		})
	}
}

// TestParseIssuerAllowlist covers the env-CSV parsing helper: empty
// strings, single + multiple entries, whitespace handling, and stray
// commas (the most common config-typo class).
func TestParseIssuerAllowlist(t *testing.T) {
	cases := []struct {
		name string
		csv  string
		want []string
	}{
		{"empty input → nil", "", nil},
		{"single entry", "https://gh.io", []string{"https://gh.io"}},
		{"two entries", "https://gh.io,https://gl.io", []string{"https://gh.io", "https://gl.io"}},
		{"leading whitespace trimmed", "  https://gh.io", []string{"https://gh.io"}},
		{"trailing whitespace trimmed", "https://gh.io  ", []string{"https://gh.io"}},
		{"whitespace around comma", "https://gh.io , https://gl.io", []string{"https://gh.io", "https://gl.io"}},
		{"consecutive commas dropped", "https://gh.io,,https://gl.io", []string{"https://gh.io", "https://gl.io"}},
		{"trailing comma dropped", "https://gh.io,", []string{"https://gh.io"}},
		{"only commas → empty", ",,,", []string{}},
		{"only whitespace → empty", "   ", []string{}},

		// SEC-057 #2: scheme-less entries are config typos and are
		// dropped (fail-closed) — a bare host could never prefix-match a
		// real `https://` issuer anyway.
		{"bare host dropped", "token.actions.githubusercontent.com", []string{}},
		{"bare host dropped, https kept", "badhost.com,https://gh.io", []string{"https://gh.io"}},
		{"http scheme kept (local dev IdP)", "http://localhost:8080", []string{"http://localhost:8080"}},
		{"scheme substring in path is not a scheme", "ttps://gh.io", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseIssuerAllowlist(tc.csv)
			// reflect.DeepEqual treats nil and empty slice as different;
			// we accept both for the "→ empty" cases by normalising.
			if tc.want == nil {
				if got != nil {
					t.Errorf("parseIssuerAllowlist(%q) = %v, want nil", tc.csv, got)
				}
				return
			}
			// Normalise: an empty want slice should match an empty (or nil) got.
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseIssuerAllowlist(%q) = %v, want %v", tc.csv, got, tc.want)
			}
		})
	}
}
