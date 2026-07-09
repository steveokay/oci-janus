package prregistry

// github.go — GitHub PR webhook payload parsing (FUT-023 §7.2).
//
// We deliberately decode only the handful of fields the lifecycle acts on
// (action, PR number, merged flag, repo name). encoding/json ignores unknown
// fields by default, so the large GitHub payload is parsed cheaply and stays
// forward-compatible with fields GitHub adds later.

import (
	"encoding/json"
	"fmt"
)

// githubPREvent is the minimal projection of a GitHub `pull_request` webhook
// payload that the dispatch needs.
//
//   - Action    — "opened" / "reopened" / "closed" / "synchronize" / ...
//   - Number    — the PR number (used in the derived namespace name).
//   - PullRequest.Merged — true when a `closed` action closed via merge (vs.
//     abandon). Distinguishes promote-and-teardown from plain teardown.
//   - Repository.FullName — "owner/repo" (used as the source_repo key).
//   - Repository.Name — "repo" (the leaf name the namespace is derived from).
type githubPREvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Merged bool `json:"merged"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
	} `json:"repository"`
}

// parseGitHubPR decodes a GitHub `pull_request` webhook body into the minimal
// githubPREvent projection. Unknown fields are ignored. A body that isn't
// well-formed JSON returns an error (the dispatch maps this to Ignored, never
// a 500 — a malformed webhook is the sender's problem, not ours).
func parseGitHubPR(rawBody []byte) (*githubPREvent, error) {
	var evt githubPREvent
	if err := json.Unmarshal(rawBody, &evt); err != nil {
		return nil, fmt.Errorf("parse github pr payload: %w", err)
	}
	return &evt, nil
}
