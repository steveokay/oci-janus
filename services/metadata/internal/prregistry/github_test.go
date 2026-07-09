package prregistry

import "testing"

func TestParseGitHubPR(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAction string
		wantNumber int
		wantMerged bool
		wantRepo   string
		wantFull   string
		wantErr    bool
	}{
		{
			name:       "opened",
			body:       `{"action":"opened","number":7,"pull_request":{"merged":false},"repository":{"full_name":"acme/backend","name":"backend"}}`,
			wantAction: "opened",
			wantNumber: 7,
			wantRepo:   "backend",
			wantFull:   "acme/backend",
		},
		{
			name:       "closed merged",
			body:       `{"action":"closed","number":9,"pull_request":{"merged":true},"repository":{"full_name":"acme/api","name":"api"}}`,
			wantAction: "closed",
			wantNumber: 9,
			wantMerged: true,
			wantRepo:   "api",
			wantFull:   "acme/api",
		},
		{
			name:       "closed unmerged",
			body:       `{"action":"closed","number":9,"pull_request":{"merged":false},"repository":{"full_name":"acme/api","name":"api"}}`,
			wantAction: "closed",
			wantNumber: 9,
			wantMerged: false,
			wantRepo:   "api",
		},
		{
			name:       "synchronize ignores unknown fields",
			body:       `{"action":"synchronize","number":1,"extra":{"deep":[1,2]},"pull_request":{"merged":false,"title":"x"},"repository":{"full_name":"a/b","name":"b","private":true}}`,
			wantAction: "synchronize",
			wantNumber: 1,
			wantRepo:   "b",
		},
		{
			name:    "malformed json",
			body:    `{"action":`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr, err := parseGitHubPR([]byte(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseGitHubPR() want error, got %+v", pr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitHubPR() unexpected error: %v", err)
			}
			if pr.Action != c.wantAction {
				t.Errorf("Action = %q, want %q", pr.Action, c.wantAction)
			}
			if pr.Number != c.wantNumber {
				t.Errorf("Number = %d, want %d", pr.Number, c.wantNumber)
			}
			if pr.PullRequest.Merged != c.wantMerged {
				t.Errorf("Merged = %v, want %v", pr.PullRequest.Merged, c.wantMerged)
			}
			if pr.Repository.Name != c.wantRepo {
				t.Errorf("Repo.Name = %q, want %q", pr.Repository.Name, c.wantRepo)
			}
			if c.wantFull != "" && pr.Repository.FullName != c.wantFull {
				t.Errorf("Repo.FullName = %q, want %q", pr.Repository.FullName, c.wantFull)
			}
		})
	}
}
