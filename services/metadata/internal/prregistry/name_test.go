package prregistry

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase passthrough", "myrepo", "myrepo"},
		{"uppercase lowered", "MyRepo", "myrepo"},
		{"underscores and dots replaced", "my_repo.v2", "my-repo-v2"},
		{"collapse repeats", "a___b...c", "a-b-c"},
		{"trim leading trailing", "__repo__", "repo"},
		{"slashes replaced", "owner/repo", "owner-repo"},
		{"all junk -> empty", "___", ""},
		{"unicode replaced", "café", "caf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitize(c.in); got != c.want {
				t.Fatalf("sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDeriveOrgName(t *testing.T) {
	cases := []struct {
		name      string
		repo      string
		pr        int
		want      string
		wantErr   bool
		maxLenChk bool
	}{
		{name: "normal", repo: "backend-api", pr: 42, want: "pr-backend-api-42"},
		{name: "uppercase+underscore+dot", repo: "My_Repo.Svc", pr: 7, want: "pr-my-repo-svc-7"},
		{name: "leading trailing junk", repo: "--weird--", pr: 3, want: "pr-weird-3"},
		{
			name:      "very long repo truncated",
			repo:      strings.Repeat("abcdefghij", 10), // 100 chars
			pr:        123,
			wantErr:   false,
			maxLenChk: true,
		},
		{name: "collapses to empty -> error", repo: "___", pr: 1, wantErr: true},
		{name: "empty repo -> error", repo: "", pr: 1, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := deriveOrgName(c.repo, c.pr)
			if c.wantErr {
				if err == nil {
					t.Fatalf("deriveOrgName(%q,%d) = %q, want error", c.repo, c.pr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("deriveOrgName(%q,%d) unexpected error: %v", c.repo, c.pr, err)
			}
			if !orgNameRE.MatchString(got) {
				t.Fatalf("deriveOrgName(%q,%d) = %q fails org regex", c.repo, c.pr, got)
			}
			if len(got) > maxOrgNameLen {
				t.Fatalf("deriveOrgName(%q,%d) = %q exceeds %d chars", c.repo, c.pr, got, maxOrgNameLen)
			}
			if c.want != "" && got != c.want {
				t.Fatalf("deriveOrgName(%q,%d) = %q, want %q", c.repo, c.pr, got, c.want)
			}
			if c.maxLenChk {
				// Must keep prefix + suffix intact even after truncation.
				if !strings.HasPrefix(got, "pr-") || !strings.HasSuffix(got, "-123") {
					t.Fatalf("truncated name %q lost its prefix/suffix", got)
				}
				if strings.HasSuffix(strings.TrimSuffix(got, "-123"), "-") {
					t.Fatalf("truncated name %q left a dangling dash before the suffix", got)
				}
			}
		})
	}
}

// TestDeriveOrgNameMaxInt32PR confirms a large-but-valid PR number still
// produces a valid name (the truncation edge with a huge number leaving no
// room for a repo segment is covered by the budget guard; here we assert the
// happy path with a max int32).
func TestDeriveOrgNameMaxInt32PR(t *testing.T) {
	got, err := deriveOrgName("svc", 2147483647)
	if err != nil {
		t.Fatalf("deriveOrgName with max int32 pr: %v", err)
	}
	if got != "pr-svc-2147483647" {
		t.Fatalf("got %q", got)
	}
	if !orgNameRE.MatchString(got) {
		t.Fatalf("name %q fails org regex", got)
	}
}
