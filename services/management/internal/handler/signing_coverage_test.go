// signing_coverage_test.go — tests for the workspace-wide signing coverage
// rollup (futures.md "Signing coverage rollup"). Uses the signerTestEnv
// harness (real handler.New() + fake meta/signer over bufconn); the fakes'
// coverage override globals shape the whole workspace per test.
//
// Each signer-enabled test uses a DISTINCT ?window= value so the endpoint's
// process-wide (tenant,window) TTL cache never bleeds results between tests.
package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
)

// coverageWire mirrors the JSON body the FE consumes. Declared here because
// the handler's response types are unexported.
type coverageWire struct {
	Window        int  `json:"window"`
	SignerEnabled bool `json:"signer_enabled"`
	Summary       struct {
		RepoCount                   int     `json:"repo_count"`
		ReposRequireSignature       int     `json:"repos_require_signature"`
		ReposEnforcedEmptyAllowlist int     `json:"repos_enforced_empty_allowlist"`
		WorkspaceSignedTagPct       float64 `json:"workspace_signed_tag_pct"`
	} `json:"summary"`
	Repos []struct {
		Org              string  `json:"org"`
		Repo             string  `json:"repo"`
		RequireSignature bool    `json:"require_signature"`
		TagsInWindow     int     `json:"tags_in_window"`
		SignedTags       int     `json:"signed_tags"`
		SignedPct        float64 `json:"signed_pct"`
		TrustedKeyCount  int     `json:"trusted_key_count"`
		AllowlistHealth  string  `json:"allowlist_health"`
		StaleTrustedKeys int     `json:"stale_trusted_keys"`
		RecentSigners    []struct {
			KeyID    string `json:"key_id"`
			TagCount int    `json:"tag_count"`
		} `json:"recent_signers"`
	} `json:"repos"`
}

// TestSigningCoverage_rollup exercises the full aggregation: three repos with
// different signing postures, per-digest signed/unsigned, digest dedupe, and
// the workspace summary. Uses ?window=60.
func TestSigningCoverage_rollup(t *testing.T) {
	env := newSignerTestEnv(t)

	t.Cleanup(func() {
		coverageReposOverride = nil
		coverageTagsOverride = nil
		coverageTrustedKeysOverride = nil
	})

	now := time.Now()
	coverageReposOverride = []*metadatav1.Repository{
		{RepoId: "r-a", Org: "acme", Name: "api", RequireSignature: true},
		{RepoId: "r-b", Org: "acme", Name: "web", RequireSignature: true},
		{RepoId: "r-c", Org: "acme", Name: "cli", RequireSignature: false},
	}
	coverageTagsOverride = map[string][]*metadatav1.Tag{
		"r-a": {
			{Name: "latest", ManifestDigest: "sha256:aaa", UpdatedAt: timestamppb.New(now)},
			{Name: "v1.0", ManifestDigest: "sha256:aaa", UpdatedAt: timestamppb.New(now.Add(-time.Hour))},
		},
		"r-b": {
			{Name: "latest", ManifestDigest: "sha256:bbb", UpdatedAt: timestamppb.New(now)},
			{Name: "old", ManifestDigest: "sha256:ccc", UpdatedAt: timestamppb.New(now.Add(-time.Hour))},
		},
		"r-c": {
			{Name: "latest", ManifestDigest: "sha256:ddd", UpdatedAt: timestamppb.New(now)},
		},
	}
	coverageTrustedKeysOverride = map[string][]*metadatav1.RepositoryTrustedKey{
		"r-a": {
			{KeyId: "key-signed"},
			{KeyId: "key-stale"},
		},
	}
	env.signer.signaturesByDigest = map[string][]*signerv1.Signature{
		"sha256:aaa": {{SignerId: "ci", KeyId: "key-signed", SignedAt: timestamppb.New(now)}},
		"sha256:bbb": {{SignerId: "ci", KeyId: "key-signed", SignedAt: timestamppb.New(now)}},
	}

	resp := env.get(t, "/api/v1/signing/coverage?window=60", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body coverageWire
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !body.SignerEnabled {
		t.Fatalf("expected signer_enabled=true")
	}
	if body.Summary.RepoCount != 3 {
		t.Fatalf("repo_count = %d, want 3", body.Summary.RepoCount)
	}
	if body.Summary.ReposRequireSignature != 2 {
		t.Errorf("repos_require_signature = %d, want 2", body.Summary.ReposRequireSignature)
	}
	if body.Summary.ReposEnforcedEmptyAllowlist != 1 {
		t.Errorf("repos_enforced_empty_allowlist = %d, want 1", body.Summary.ReposEnforcedEmptyAllowlist)
	}
	if got := body.Summary.WorkspaceSignedTagPct; got < 0.59 || got > 0.61 {
		t.Errorf("workspace_signed_tag_pct = %v, want ~0.6", got)
	}

	byRepo := map[string]int{}
	for i, r := range body.Repos {
		byRepo[r.Repo] = i
	}
	api := body.Repos[byRepo["api"]]
	if api.SignedTags != 2 || api.TagsInWindow != 2 {
		t.Errorf("api coverage = %d/%d, want 2/2", api.SignedTags, api.TagsInWindow)
	}
	if api.AllowlistHealth != "enforced_with_allowlist" {
		t.Errorf("api allowlist_health = %q, want enforced_with_allowlist", api.AllowlistHealth)
	}
	if api.StaleTrustedKeys != 1 {
		t.Errorf("api stale_trusted_keys = %d, want 1 (key-stale never signed)", api.StaleTrustedKeys)
	}
	web := body.Repos[byRepo["web"]]
	if web.SignedTags != 1 || web.TagsInWindow != 2 {
		t.Errorf("web coverage = %d/%d, want 1/2", web.SignedTags, web.TagsInWindow)
	}
	if web.AllowlistHealth != "enforced_any_signature" {
		t.Errorf("web allowlist_health = %q, want enforced_any_signature", web.AllowlistHealth)
	}
	cli := body.Repos[byRepo["cli"]]
	if cli.AllowlistHealth != "advisory" {
		t.Errorf("cli allowlist_health = %q, want advisory", cli.AllowlistHealth)
	}

	env.signer.mu.Lock()
	calls := env.signer.listCalls
	env.signer.mu.Unlock()
	if calls != 4 {
		t.Errorf("ListSignatures calls = %d, want 4 (one per distinct digest)", calls)
	}
}

// TestSigningCoverage_invalidWindow rejects non-integer / non-positive windows.
func TestSigningCoverage_invalidWindow(t *testing.T) {
	env := newSignerTestEnv(t)
	for _, q := range []string{"?window=abc", "?window=0", "?window=-5"} {
		resp := env.get(t, "/api/v1/signing/coverage"+q, adminToken)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("window %q: expected 400, got %d", q, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestSigningCoverage_signerUnwired returns 200 with signer_enabled=false and
// an empty repo list so the FE renders its "signing not wired" card instead of
// erroring. The signer-nil branch is checked before the cache, so this is safe
// regardless of any cached enabled response.
func TestSigningCoverage_signerUnwired(t *testing.T) {
	srv := newSignerlessTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/signing/coverage", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET coverage: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body coverageWire
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SignerEnabled {
		t.Errorf("expected signer_enabled=false")
	}
	if len(body.Repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(body.Repos))
	}
}
