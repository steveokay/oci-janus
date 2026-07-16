# Workspace Signing Coverage Rollup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a workspace-wide "Image signing coverage" rollup under Security → Signing — per-repo signed-tag %, recent signers, trusted-key allowlist health, and `require_signature` status — backed by one new read-only BFF endpoint.

**Architecture:** A single BFF route (`GET /api/v1/signing/coverage`) on `services/management` orchestrates existing gRPC calls (`meta.ListRepositories` → per-repo `meta.ListTags` + `signer.ListSignatures` + `meta.ListRepositoryTrustedKeys`), aggregates per-repo coverage over a bounded recent-tag window, and returns a workspace summary. No proto change, no DB migration. The frontend replaces the existing dashed placeholder route with a summary strip + coverage table that consumes a new `useSigningCoverage` hook.

**Tech Stack:** Go 1.25 (net/http `ServeMux`, pgx-free BFF layer, `log/slog`), gRPC; React + TypeScript, TanStack Query, TanStack Router, Vitest, Tailwind design tokens.

**Design spec:** `docs/superpowers/specs/2026-07-16-signing-coverage-rollup-design.md`

**Working branch:** `feat/signing-coverage-rollup` (already created).

---

## Conventions used throughout

- Backend package is `handler` (`services/management/internal/handler`). Tests are external (`package handler_test`) and share fakes defined in `handler_test.go` / `signature_test.go`.
- The endpoint reuses the unexported `recentSignerEntry` type already defined in `trusted_keys.go` (same package) for the `recent_signers` field — do **not** redeclare it.
- Cross-test cache isolation: the endpoint keeps a process-wide 60s TTL cache keyed by `(tenant_id, window)`. The cache is checked **after** the signer-nil degrade branch. Because the cache is unreachable from the external test package, **each backend HTTP test that exercises the signer-enabled path uses a distinct `?window=` value** so cache entries never bleed between tests. This is called out again in Task 1's test steps.

---

## File Structure

**Backend (`services/management/internal/handler/`)**
- Create: `signing_coverage.go` — the handler, response types, per-repo aggregation helpers, and the TTL cache. One responsibility: build the coverage rollup.
- Create: `signing_coverage_test.go` — external-package HTTP tests for the endpoint.
- Modify: `handler.go` — register the route in the existing signer-optional block (one line).
- Modify: `handler_test.go` — add `ListRepositoryTrustedKeys` to `fakeMetaServer` + three override globals (`coverageReposOverride`, `coverageTagsOverride`, `coverageTrustedKeysOverride`); honor the repo/tag overrides at the top of the existing `ListRepositories`/`ListTags` fakes.
- Modify: `signature_test.go` — add `signaturesByDigest` + a `listCalls` counter to `fakeSignerServer` and consult them in `ListSignatures`.

**Frontend (`frontend/src/`)**
- Create: `lib/api/signing-coverage.ts` — types, query keys, `fetchSigningCoverage`, `useSigningCoverage`.
- Create: `lib/api/signing-coverage.test.ts` — `fetchSigningCoverage` unit test.
- Create: `components/security/signing-coverage-bar.tsx` — `SigningCoverageBar`.
- Create: `components/security/__tests__/signing-coverage-bar.test.tsx`.
- Create: `components/security/signing-coverage-summary.tsx` — `SigningCoverageSummary`.
- Create: `components/security/signing-coverage-table.tsx` — `SigningCoverageTable` (+ `AllowlistHealthBadge`).
- Create: `components/security/__tests__/signing-coverage-table.test.tsx`.
- Modify: `routes/_authenticated.security.signing.tsx` — replace the placeholder body with the live tab.

**Docs / trackers**
- Modify: `docs/SIGNING.md`, `README.md`, `futures.md`, `status.md`, `status-tracker.md`.

---

## Task 1: BFF signing-coverage endpoint

**Files:**
- Create: `services/management/internal/handler/signing_coverage.go`
- Create: `services/management/internal/handler/signing_coverage_test.go`
- Modify: `services/management/internal/handler/handler.go` (route registration, in the signer-optional block near line 470)
- Modify: `services/management/internal/handler/handler_test.go` (fake meta overrides + `ListRepositoryTrustedKeys`)
- Modify: `services/management/internal/handler/signature_test.go` (`fakeSignerServer` per-digest signatures + call counter)

- [ ] **Step 1: Extend the fake signer** (`signature_test.go`)

Add two fields to the `fakeSignerServer` struct (after the `signatures` field, ~line 46):

```go
	// signaturesByDigest, when non-nil, makes ListSignatures answer per
	// manifest_digest: a hit returns those signatures, a miss returns
	// NotFound (i.e. an unsigned manifest). Lets the coverage rollup tests
	// mark specific digests signed vs unsigned. Takes precedence over
	// `signatures` when set.
	signaturesByDigest map[string][]*signerv1.Signature

	// listCalls counts ListSignatures invocations so the coverage test can
	// assert per-digest dedupe (two tags → one manifest → one call).
	listCalls int
```

Replace the body of `ListSignatures` (currently ~lines 68-86) with:

```go
func (s *fakeSignerServer) ListSignatures(_ context.Context, req *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
	s.mu.Lock()
	s.listCalls++
	s.mu.Unlock()

	if s.listErr != nil {
		return nil, s.listErr
	}
	// Per-digest table wins when configured (coverage rollup tests).
	if s.signaturesByDigest != nil {
		sigs, ok := s.signaturesByDigest[req.GetManifestDigest()]
		if !ok || len(sigs) == 0 {
			return nil, status.Error(codes.NotFound, "no signatures for digest")
		}
		return &signerv1.ListSignaturesResponse{Signatures: sigs}, nil
	}
	if s.signatures == nil {
		return &signerv1.ListSignaturesResponse{
			Signatures: []*signerv1.Signature{
				{
					SignerId:        "signer-A",
					KeyId:           "key-A",
					SignatureDigest: "sha256:sigA",
					ManifestDigest:  "sha256:abc123",
					SignedAt:        timestamppb.Now(),
				},
			},
		}, nil
	}
	return &signerv1.ListSignaturesResponse{Signatures: s.signatures}, nil
}
```

Ensure `signature_test.go` imports `"google.golang.org/grpc/codes"` and `"google.golang.org/grpc/status"` (add them to the import block if absent).

- [ ] **Step 2: Extend the fake metadata server** (`handler_test.go`)

Add these package-level override vars near the other override globals (e.g. after `storageBreakdownOverride`, ~line 238):

```go
// Coverage-rollup test hooks (signing_coverage_test.go). When non-nil the
// fake streams the override set instead of its canned defaults so a single
// test can shape the whole workspace.
var (
	coverageReposOverride       []*metadatav1.Repository
	coverageTagsOverride        map[string][]*metadatav1.Tag                  // repo_id → tags
	coverageTrustedKeysOverride map[string][]*metadatav1.RepositoryTrustedKey // repo_id → keys
)
```

At the **top** of the existing `ListRepositories` (line 261), before the two canned `stream.Send` calls, add:

```go
	if coverageReposOverride != nil {
		for _, repo := range coverageReposOverride {
			_ = stream.Send(repo)
		}
		return nil
	}
```

At the **top** of the existing `ListTags` (line 307), before the canned `stream.Send`, add:

```go
	if coverageTagsOverride != nil {
		for _, tag := range coverageTagsOverride[req.GetRepoId()] {
			_ = stream.Send(tag)
		}
		return nil
	}
```

Add a new `ListRepositoryTrustedKeys` method to `fakeMetaServer` (anywhere among its methods):

```go
func (s *fakeMetaServer) ListRepositoryTrustedKeys(_ context.Context, req *metadatav1.ListRepositoryTrustedKeysRequest) (*metadatav1.ListRepositoryTrustedKeysResponse, error) {
	if coverageTrustedKeysOverride != nil {
		return &metadatav1.ListRepositoryTrustedKeysResponse{Keys: coverageTrustedKeysOverride[req.GetRepoId()]}, nil
	}
	return &metadatav1.ListRepositoryTrustedKeysResponse{}, nil
}
```

- [ ] **Step 3: Write the failing endpoint test** (`signing_coverage_test.go`)

```go
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

	// Reset overrides after the test so other tests see canned defaults.
	t.Cleanup(func() {
		coverageReposOverride = nil
		coverageTagsOverride = nil
		coverageTrustedKeysOverride = nil
	})

	now := time.Now()
	coverageReposOverride = []*metadatav1.Repository{
		{RepoId: "r-a", Org: "acme", Name: "api", RequireSignature: true},   // enforced + allowlist
		{RepoId: "r-b", Org: "acme", Name: "web", RequireSignature: true},   // enforced, EMPTY allowlist
		{RepoId: "r-c", Org: "acme", Name: "cli", RequireSignature: false},  // advisory
	}
	coverageTagsOverride = map[string][]*metadatav1.Tag{
		// r-a: two tags, both point at the SAME signed digest → 2/2 signed,
		// and ListSignatures must be called ONCE for that digest (dedupe).
		"r-a": {
			{Name: "latest", ManifestDigest: "sha256:aaa", UpdatedAt: timestamppb.New(now)},
			{Name: "v1.0", ManifestDigest: "sha256:aaa", UpdatedAt: timestamppb.New(now.Add(-time.Hour))},
		},
		// r-b: two tags, one signed one unsigned → 1/2 signed.
		"r-b": {
			{Name: "latest", ManifestDigest: "sha256:bbb", UpdatedAt: timestamppb.New(now)},
			{Name: "old", ManifestDigest: "sha256:ccc", UpdatedAt: timestamppb.New(now.Add(-time.Hour))},
		},
		// r-c: one unsigned tag → 0/1 signed.
		"r-c": {
			{Name: "latest", ManifestDigest: "sha256:ddd", UpdatedAt: timestamppb.New(now)},
		},
	}
	coverageTrustedKeysOverride = map[string][]*metadatav1.RepositoryTrustedKey{
		// r-a trusts key-signed (which DID sign) plus key-stale (which did not).
		"r-a": {
			{KeyId: "key-signed"},
			{KeyId: "key-stale"},
		},
	}
	env.signer.signaturesByDigest = map[string][]*signerv1.Signature{
		"sha256:aaa": {{SignerId: "ci", KeyId: "key-signed", SignedAt: timestamppb.New(now)}},
		"sha256:bbb": {{SignerId: "ci", KeyId: "key-signed", SignedAt: timestamppb.New(now)}},
		// ccc, ddd absent → unsigned.
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
	// Workspace: signed tags = 2 (r-a) + 1 (r-b) + 0 (r-c) = 3; total = 2+2+1 = 5.
	if got := body.Summary.WorkspaceSignedTagPct; got < 0.59 || got > 0.61 {
		t.Errorf("workspace_signed_tag_pct = %v, want ~0.6", got)
	}

	// Repos are sorted by (org, repo): api, cli, web.
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

	// Digest dedupe: r-a's two tags share sha256:aaa. Across the whole
	// workspace ListSignatures is called once per DISTINCT digest:
	// aaa, bbb, ccc, ddd = 4 calls.
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
```

Add a signer-unwired degrade test that mirrors the existing signerless pattern in `signature_test.go` (the file already builds a signerless server via `newSignerlessTestEnv` and GETs it — follow that exact call shape for issuing the request + token):

```go
// TestSigningCoverage_signerUnwired returns 200 with signer_enabled=false and
// an empty repo list so the FE renders its "signing not wired" card instead of
// erroring. The signer-nil branch is checked before the cache, so this is safe
// regardless of any cached enabled response.
func TestSigningCoverage_signerUnwired(t *testing.T) {
	srv := newSignerlessTestEnv(t)
	// Issue GET /api/v1/signing/coverage against srv with adminToken using the
	// same request helper the sibling signerless test in signature_test.go uses.
	// Assert: status 200, body.signer_enabled == false, len(body.repos) == 0.
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd services/management && go test ./internal/handler/ -run TestSigningCoverage -v`
Expected: compile failure or FAIL — the route/handler does not exist yet, so requests 404 and assertions fail. (If it does not even compile because `handleSigningCoverage` is unreferenced, that is fine — Step 5 adds it.)

- [ ] **Step 5: Implement the handler** — create `signing_coverage.go`

```go
// Package handler — signing_coverage.go
//
// Workspace-wide image-signing coverage rollup for the Security → Signing
// tab (futures.md "Signing coverage rollup"). This is a VISIBILITY feature —
// it changes no admission decision. It is deliberately separate from the
// deferred "Signed-image admission Phase 3" enforcement work (quorum,
// rotation, keyless).
//
//	GET /api/v1/signing/coverage?window=50   — reader-allowed
//
// Pure BFF orchestration over existing gRPC (mirrors the recent-signers
// route): ListRepositories → per-repo ListTags + signer.ListSignatures +
// ListRepositoryTrustedKeys. Coverage is computed over the N most-recent
// tags per repo (bounded fan-out); the window is disclosed in the response
// so the percentage is never mistaken for all-tags. A process-wide 60s TTL
// cache keyed by (tenant,window) shields the backend from repeated fan-out.
package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

const (
	defaultCoverageWindow    = 50
	maxCoverageWindow        = 200
	maxReposPerRollup        = 1000 // safety cap on the repo enumeration
	coverageFanoutWorkers    = 8
	coverageSignerTimeout    = 3 * time.Second
	maxCoverageRecentSigners = 10

	allowlistHealthEnforcedWithAllowlist = "enforced_with_allowlist"
	allowlistHealthEnforcedAnySignature  = "enforced_any_signature"
	allowlistHealthAdvisory              = "advisory"
)

// signingCoverageResponse is the JSON wire body. `recent_signers` reuses the
// recentSignerEntry type defined in trusted_keys.go (same package).
type signingCoverageResponse struct {
	Window        int                    `json:"window"`
	SignerEnabled bool                   `json:"signer_enabled"`
	Summary       signingCoverageSummary `json:"summary"`
	Repos         []repoCoverage         `json:"repos"`
}

type signingCoverageSummary struct {
	RepoCount                   int     `json:"repo_count"`
	ReposRequireSignature       int     `json:"repos_require_signature"`
	ReposEnforcedEmptyAllowlist int     `json:"repos_enforced_empty_allowlist"`
	WorkspaceSignedTagPct       float64 `json:"workspace_signed_tag_pct"`
}

type repoCoverage struct {
	Org              string              `json:"org"`
	Repo             string              `json:"repo"`
	RequireSignature bool                `json:"require_signature"`
	Window           int                 `json:"window"`
	TagsInWindow     int                 `json:"tags_in_window"`
	SignedTags       int                 `json:"signed_tags"`
	SignedPct        float64             `json:"signed_pct"`
	TrustedKeyCount  int                 `json:"trusted_key_count"`
	AllowlistHealth  string              `json:"allowlist_health"`
	StaleTrustedKeys int                 `json:"stale_trusted_keys"`
	RecentSigners    []recentSignerEntry `json:"recent_signers"`
}

// --- TTL cache -------------------------------------------------------------

type coverageCacheEntry struct {
	resp    signingCoverageResponse
	expires time.Time
}

type coverageTTLCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]coverageCacheEntry
}

func newCoverageTTLCache(ttl time.Duration) *coverageTTLCache {
	return &coverageTTLCache{ttl: ttl, entries: make(map[string]coverageCacheEntry)}
}

func (c *coverageTTLCache) get(key string) (signingCoverageResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return signingCoverageResponse{}, false
	}
	return e.resp, true
}

func (c *coverageTTLCache) set(key string, resp signingCoverageResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = coverageCacheEntry{resp: resp, expires: time.Now().Add(c.ttl)}
}

// Process-wide cache. 60s balances freshness against the cost of re-fanning
// out across every repo on each dashboard load.
var signingCoverageCache = newCoverageTTLCache(60 * time.Second)

// --- handler ---------------------------------------------------------------

func (h *Handler) handleSigningCoverage(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	window := defaultCoverageWindow
	if raw := r.URL.Query().Get("window"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid window (expected positive integer)")
			return
		}
		if n > maxCoverageWindow {
			n = maxCoverageWindow
		}
		window = n
	}

	// Signer not wired (SIGNER_GRPC_ADDR unset) → degrade to a 200 body with
	// signer_enabled=false and no repos. Checked BEFORE the cache so this path
	// never serves, and is never served by, a cached enabled response.
	if h.signer == nil {
		writeJSON(w, http.StatusOK, signingCoverageResponse{
			Window:        window,
			SignerEnabled: false,
			Summary:       signingCoverageSummary{},
			Repos:         []repoCoverage{},
		})
		return
	}

	cacheKey := tenantID + "|" + strconv.Itoa(window)
	if cached, ok := signingCoverageCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	repos, err := h.listAllReposForCoverage(r.Context(), tenantID)
	if err != nil {
		slog.Error("signing-coverage ListRepositories", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list repositories")
		return
	}

	// Bounded fan-out: one worker slot per repo, capped at coverageFanoutWorkers.
	results := make([]repoCoverage, len(repos))
	sem := make(chan struct{}, coverageFanoutWorkers)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, rp *metadatav1.Repository) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = h.coverageForRepo(r.Context(), tenantID, rp, window)
		}(i, repo)
	}
	wg.Wait()

	// Deterministic order (goroutines complete out of order) → stable UI + tests.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Org != results[j].Org {
			return results[i].Org < results[j].Org
		}
		return results[i].Repo < results[j].Repo
	})

	resp := signingCoverageResponse{
		Window:        window,
		SignerEnabled: true,
		Summary:       summarizeCoverage(results),
		Repos:         results,
	}
	signingCoverageCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// listAllReposForCoverage drains the ListRepositories stream once. metadata
// returns the tenant's repos in a single server stream today (no page-token
// loop), so we request a high page size and read to EOF. If we ever hit the
// cap we log rather than silently truncate.
func (h *Handler) listAllReposForCoverage(ctx context.Context, tenantID string) ([]*metadatav1.Repository, error) {
	stream, err := h.meta.ListRepositories(ctx, &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
		PageSize: maxReposPerRollup,
	})
	if err != nil {
		return nil, err
	}
	var out []*metadatav1.Repository
	for {
		repo, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		out = append(out, repo)
	}
	if len(out) >= maxReposPerRollup {
		slog.Warn("signing-coverage repo list may be truncated", "cap", maxReposPerRollup)
	}
	return out, nil
}

type coverageTag struct {
	digest    string
	updatedAt time.Time
}

type coverageSignerAgg struct {
	keyID    string
	signerID string
	latest   time.Time
	tags     map[string]struct{} // distinct signed manifest digests for this key
}

// coverageForRepo aggregates one repo's signing posture over the window. Every
// downstream error is fail-open (log + degrade that field) so one flaky repo
// never fails the whole rollup.
func (h *Handler) coverageForRepo(ctx context.Context, tenantID string, repo *metadatav1.Repository, window int) repoCoverage {
	label := repo.GetOrg() + "/" + repo.GetName()
	rc := repoCoverage{
		Org:              repo.GetOrg(),
		Repo:             repo.GetName(),
		RequireSignature: repo.GetRequireSignature(),
		Window:           window,
		AllowlistHealth:  allowlistHealthAdvisory,
		RecentSigners:    []recentSignerEntry{},
	}

	tags := h.recentTagDigests(ctx, tenantID, repo.GetRepoId(), window)
	rc.TagsInWindow = len(tags)

	// One ListSignatures call per DISTINCT digest (two tags → one manifest).
	digestSigned := make(map[string]bool)
	digestSigs := make(map[string][]*signerv1.Signature)
	for _, t := range tags {
		if _, done := digestSigned[t.digest]; done {
			continue
		}
		sigs := h.coverageSignatures(ctx, tenantID, t.digest, label)
		digestSigs[t.digest] = sigs
		digestSigned[t.digest] = len(sigs) > 0
	}

	// signed_tags counts per TAG (a manifest shared by two tags counts twice).
	signedKeyIDs := make(map[string]struct{})
	agg := make(map[string]*coverageSignerAgg)
	for _, t := range tags {
		if digestSigned[t.digest] {
			rc.SignedTags++
		}
		for _, s := range digestSigs[t.digest] {
			kid := s.GetKeyId()
			if kid == "" {
				continue
			}
			signedKeyIDs[kid] = struct{}{}
			signedAt := s.GetSignedAt().AsTime()
			e, ok := agg[kid]
			if !ok {
				e = &coverageSignerAgg{keyID: kid, signerID: s.GetSignerId(), latest: signedAt, tags: map[string]struct{}{}}
				agg[kid] = e
			}
			if e.signerID == "" {
				e.signerID = s.GetSignerId()
			}
			if signedAt.After(e.latest) {
				e.latest = signedAt
			}
			e.tags[t.digest] = struct{}{}
		}
	}
	if rc.TagsInWindow > 0 {
		rc.SignedPct = float64(rc.SignedTags) / float64(rc.TagsInWindow)
	}

	keys := h.coverageTrustedKeys(ctx, tenantID, repo.GetRepoId(), label)
	rc.TrustedKeyCount = len(keys)
	for _, k := range keys {
		if _, seen := signedKeyIDs[k.GetKeyId()]; !seen {
			rc.StaleTrustedKeys++
		}
	}

	switch {
	case !rc.RequireSignature:
		rc.AllowlistHealth = allowlistHealthAdvisory
	case rc.TrustedKeyCount == 0:
		// require_signature on but nothing to intersect against → ANY signature
		// passes admission. The soft spot operators miss.
		rc.AllowlistHealth = allowlistHealthEnforcedAnySignature
	default:
		rc.AllowlistHealth = allowlistHealthEnforcedWithAllowlist
	}

	rc.RecentSigners = flattenCoverageSigners(agg)
	return rc
}

func (h *Handler) recentTagDigests(ctx context.Context, tenantID, repoID string, window int) []coverageTag {
	stream, err := h.meta.ListTags(ctx, &metadatav1.ListTagsRequest{
		TenantId: tenantID,
		RepoId:   repoID,
		PageSize: int32(window), //nolint:gosec // window bounded to [1, maxCoverageWindow]
	})
	if err != nil {
		slog.Warn("signing-coverage ListTags", "err", err, "repo_id", repoID)
		return nil
	}
	var tags []coverageTag
	for {
		tag, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			slog.Warn("signing-coverage ListTags stream", "err", recvErr, "repo_id", repoID)
			break
		}
		// Skip referrer artifacts (their "signature" points at the parent).
		if at := tag.GetArtifactType(); at == "signature" || at == "sbom" {
			continue
		}
		d := tag.GetManifestDigest()
		if d == "" {
			continue
		}
		tags = append(tags, coverageTag{digest: d, updatedAt: tag.GetUpdatedAt().AsTime()})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].updatedAt.After(tags[j].updatedAt) })
	if len(tags) > window {
		tags = tags[:window]
	}
	return tags
}

func (h *Handler) coverageSignatures(ctx context.Context, tenantID, digest, label string) []*signerv1.Signature {
	callCtx, cancel := context.WithTimeout(ctx, coverageSignerTimeout)
	defer cancel()
	resp, err := h.signer.ListSignatures(callCtx, &signerv1.ListSignaturesRequest{
		ManifestDigest: digest,
		TenantId:       tenantID,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil // unsigned manifest — normal
		}
		slog.Warn("signing-coverage ListSignatures", "err", err, "repo", label, "digest", digest)
		return nil
	}
	return resp.GetSignatures()
}

func (h *Handler) coverageTrustedKeys(ctx context.Context, tenantID, repoID, label string) []*metadatav1.RepositoryTrustedKey {
	resp, err := h.meta.ListRepositoryTrustedKeys(ctx, &metadatav1.ListRepositoryTrustedKeysRequest{
		TenantId: tenantID,
		RepoId:   repoID,
	})
	if err != nil {
		slog.Warn("signing-coverage ListRepositoryTrustedKeys", "err", err, "repo", label)
		return nil
	}
	return resp.GetKeys()
}

func flattenCoverageSigners(agg map[string]*coverageSignerAgg) []recentSignerEntry {
	out := make([]recentSignerEntry, 0, len(agg))
	for _, e := range agg {
		out = append(out, recentSignerEntry{
			KeyID:        e.keyID,
			SignerID:     e.signerID,
			LastSignedAt: e.latest,
			TagCount:     len(e.tags),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSignedAt.After(out[j].LastSignedAt) })
	if len(out) > maxCoverageRecentSigners {
		out = out[:maxCoverageRecentSigners]
	}
	return out
}

func summarizeCoverage(repos []repoCoverage) signingCoverageSummary {
	s := signingCoverageSummary{RepoCount: len(repos)}
	var totalTags, totalSigned int
	for _, r := range repos {
		if r.RequireSignature {
			s.ReposRequireSignature++
		}
		if r.AllowlistHealth == allowlistHealthEnforcedAnySignature {
			s.ReposEnforcedEmptyAllowlist++
		}
		totalTags += r.TagsInWindow
		totalSigned += r.SignedTags
	}
	if totalTags > 0 {
		s.WorkspaceSignedTagPct = float64(totalSigned) / float64(totalTags)
	}
	return s
}
```

> **Verify against the proto before compiling:** confirm `recentSignerEntry`'s field names/types in `trusted_keys.go` (`KeyID`, `SignerID`, `LastSignedAt time.Time`, `TagCount int`) and that `RepositoryTrustedKey` exposes `GetKeyId()`. Both were checked during design; adjust only if the generated code differs.

- [ ] **Step 6: Register the route** (`handler.go`, in the signer-optional block near line 470)

Add after the recent-signers registration:

```go
	// Workspace-wide signing coverage rollup (futures.md "Signing coverage
	// rollup"). Reader-allowed — posture data, same bar as trusted-keys List.
	mux.Handle("GET /api/v1/signing/coverage", authMW(http.HandlerFunc(h.handleSigningCoverage)))
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd services/management && go test ./internal/handler/ -run TestSigningCoverage -v`
Expected: PASS for `TestSigningCoverage_rollup`, `TestSigningCoverage_invalidWindow`, `TestSigningCoverage_signerUnwired`.

- [ ] **Step 8: Run the full handler package + vet/lint**

Run: `cd services/management && go test ./... && go vet ./... && gofmt -l .`
Expected: all tests pass, no vet errors, `gofmt -l` prints nothing. Then run the service target: `make -C services/management` (or the root aggregate). Fix any `golangci-lint` findings in code you touched.

- [ ] **Step 9: Commit**

```bash
git add services/management/internal/handler/signing_coverage.go \
        services/management/internal/handler/signing_coverage_test.go \
        services/management/internal/handler/handler.go \
        services/management/internal/handler/handler_test.go \
        services/management/internal/handler/signature_test.go
git commit -m "feat(management): workspace signing coverage rollup endpoint"
```

---

## Task 2: Frontend API hook

**Files:**
- Create: `frontend/src/lib/api/signing-coverage.ts`
- Test: `frontend/src/lib/api/signing-coverage.test.ts`

- [ ] **Step 1: Write the failing test** (`signing-coverage.test.ts`)

```ts
import { describe, it, expect, vi, beforeEach } from "vitest";

// Beacon — signing-coverage.ts fetch test. Mirrors auth.test.ts: mock the
// axios client and assert fetchSigningCoverage hits the right URL + params
// and returns the body verbatim.

const get = vi.fn();
vi.mock("./client", () => ({
  apiClient: { get: (...args: unknown[]) => get(...args) },
}));

import { fetchSigningCoverage } from "./signing-coverage";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("fetchSigningCoverage()", () => {
  it("requests /signing/coverage with the window param and returns the body", async () => {
    const body = {
      window: 50,
      signer_enabled: true,
      summary: {
        repo_count: 1,
        repos_require_signature: 1,
        repos_enforced_empty_allowlist: 0,
        workspace_signed_tag_pct: 1,
      },
      repos: [],
    };
    get.mockResolvedValueOnce({ data: body });

    const result = await fetchSigningCoverage(50);

    expect(get).toHaveBeenCalledWith("/signing/coverage", { params: { window: 50 } });
    expect(result).toEqual(body);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npx vitest run src/lib/api/signing-coverage.test.ts`
Expected: FAIL — `signing-coverage.ts` does not exist.

- [ ] **Step 3: Implement the hook** (`signing-coverage.ts`)

```ts
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

// Beacon — workspace signing coverage rollup (futures.md "Signing coverage
// rollup"). Read-only aggregate powering the Security → Signing tab. The fetch
// is split out as a plain async fn so it can be unit-tested without a
// QueryClient (see signing-coverage.test.ts), mirroring auth.ts.

export type AllowlistHealth =
  | "enforced_with_allowlist"
  | "enforced_any_signature"
  | "advisory";

export interface CoverageSigner {
  key_id: string;
  signer_id?: string;
  last_signed_at: string;
  tag_count: number;
}

export interface RepoCoverage {
  org: string;
  repo: string;
  require_signature: boolean;
  window: number;
  tags_in_window: number;
  signed_tags: number;
  signed_pct: number;
  trusted_key_count: number;
  allowlist_health: AllowlistHealth;
  stale_trusted_keys: number;
  recent_signers: CoverageSigner[];
}

export interface SigningCoverageSummary {
  repo_count: number;
  repos_require_signature: number;
  repos_enforced_empty_allowlist: number;
  workspace_signed_tag_pct: number;
}

export interface SigningCoverage {
  window: number;
  signer_enabled: boolean;
  summary: SigningCoverageSummary;
  repos: RepoCoverage[];
}

export const signingCoverageKeys = {
  all: ["signing-coverage"] as const,
  rollup: (window: number) => [...signingCoverageKeys.all, window] as const,
};

export async function fetchSigningCoverage(window: number): Promise<SigningCoverage> {
  const { data } = await apiClient.get<SigningCoverage>("/signing/coverage", {
    params: { window },
  });
  return data;
}

export function useSigningCoverage(window = 50) {
  return useQuery({
    queryKey: signingCoverageKeys.rollup(window),
    queryFn: () => fetchSigningCoverage(window),
    staleTime: 60_000,
  });
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && npx vitest run src/lib/api/signing-coverage.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/api/signing-coverage.ts frontend/src/lib/api/signing-coverage.test.ts
git commit -m "feat(frontend): useSigningCoverage hook + types"
```

---

## Task 3: CoverageBar component

**Files:**
- Create: `frontend/src/components/security/signing-coverage-bar.tsx`
- Test: `frontend/src/components/security/__tests__/signing-coverage-bar.test.tsx`

- [ ] **Step 1: Write the failing test**

```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SigningCoverageBar } from "../signing-coverage-bar";

describe("SigningCoverageBar", () => {
  it("renders the signed/total label and an accessible percentage", () => {
    render(<SigningCoverageBar pct={0.95} signed={38} total={40} />);
    expect(screen.getByText("38/40")).toBeInTheDocument();
    expect(screen.getByRole("img")).toHaveAttribute(
      "aria-label",
      "95% signed (38 of 40 tags)",
    );
  });

  it("uses the danger tone below 50% coverage", () => {
    render(<SigningCoverageBar pct={0.2} signed={1} total={5} />);
    // The filled segment carries the danger token class.
    const fill = document.querySelector("span.bg-\\[var\\(--color-danger\\)\\]");
    expect(fill).not.toBeNull();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npx vitest run src/components/security/__tests__/signing-coverage-bar.test.tsx`
Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement the component**

```tsx
import * as React from "react";
import { cn } from "@/lib/utils";

// Beacon — SigningCoverageBar. Single-value progress bar for a repo's
// signed-tag coverage, color-coded so weak repos are scannable at a glance.
// Mirrors SeverityBar's token + 2px-floor conventions.

const GOOD = 0.9;
const WARN = 0.5;

function toneFor(pct: number): { bar: string; label: string } {
  if (pct >= GOOD) return { bar: "bg-[var(--color-success)]", label: "text-[var(--color-success)]" };
  if (pct >= WARN) return { bar: "bg-[var(--color-warning)]", label: "text-[var(--color-warning)]" };
  return { bar: "bg-[var(--color-danger)]", label: "text-[var(--color-danger)]" };
}

interface SigningCoverageBarProps {
  pct: number;
  signed: number;
  total: number;
  className?: string;
}

export function SigningCoverageBar({
  pct,
  signed,
  total,
  className,
}: SigningCoverageBarProps): React.ReactElement {
  const clamped = Math.max(0, Math.min(1, pct));
  const tone = toneFor(clamped);
  const pctText = `${Math.round(clamped * 100)}%`;
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <div
        role="img"
        aria-label={`${pctText} signed (${signed} of ${total} tags)`}
        className="h-2 w-24 overflow-hidden rounded-full bg-[var(--color-surface-sunken)]"
      >
        <span
          className={cn("block h-full transition-all", tone.bar)}
          style={{ width: `max(${clamped * 100}%, 2px)` }}
        />
      </div>
      <span className={cn("tabular-nums text-xs font-medium", tone.label)}>
        {signed}/{total}
      </span>
    </div>
  );
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && npx vitest run src/components/security/__tests__/signing-coverage-bar.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/security/signing-coverage-bar.tsx frontend/src/components/security/__tests__/signing-coverage-bar.test.tsx
git commit -m "feat(frontend): SigningCoverageBar"
```

---

## Task 4: Summary strip + coverage table components

**Files:**
- Create: `frontend/src/components/security/signing-coverage-summary.tsx`
- Create: `frontend/src/components/security/signing-coverage-table.tsx`
- Test: `frontend/src/components/security/__tests__/signing-coverage-table.test.tsx`

- [ ] **Step 1: Write the failing test** (`signing-coverage-table.test.tsx`)

```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "@tanstack/react-router"; // if unavailable, see note below
import type { RepoCoverage } from "@/lib/api/signing-coverage";
import { SigningCoverageTable } from "../signing-coverage-table";

const repos: RepoCoverage[] = [
  {
    org: "acme",
    repo: "api",
    require_signature: true,
    window: 50,
    tags_in_window: 40,
    signed_tags: 38,
    signed_pct: 0.95,
    trusted_key_count: 2,
    allowlist_health: "enforced_with_allowlist",
    stale_trusted_keys: 0,
    recent_signers: [{ key_id: "key-1", last_signed_at: "2026-07-16T00:00:00Z", tag_count: 12 }],
  },
  {
    org: "acme",
    repo: "web",
    require_signature: true,
    window: 50,
    tags_in_window: 10,
    signed_tags: 3,
    signed_pct: 0.3,
    trusted_key_count: 0,
    allowlist_health: "enforced_any_signature",
    stale_trusted_keys: 0,
    recent_signers: [],
  },
];

describe("SigningCoverageTable", () => {
  it("renders a row per repo with coverage and health", () => {
    render(<SigningCoverageTable repos={repos} />);
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
    expect(screen.getByText("38/40")).toBeInTheDocument();
    // Empty-allowlist repos surface the "any signature" health label.
    expect(screen.getByText(/any signature/i)).toBeInTheDocument();
  });
});
```

> **Router note:** If `SigningCoverageTable`'s drill-in uses TanStack Router's `<Link>`, the test must wrap it in a router provider. Check how `repositories-table.test.tsx` renders `<Link>`-containing tables and copy that exact wrapper (it may use a test `RouterProvider` or a stub). If wrapping is heavy, make the drill-in a plain `<a href>` built from the org/repo and drop the wrapper — the destination is `/repositories/{org}/{repo}/settings`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npx vitest run src/components/security/__tests__/signing-coverage-table.test.tsx`
Expected: FAIL — components do not exist.

- [ ] **Step 3: Implement `signing-coverage-summary.tsx`**

```tsx
import * as React from "react";
import { cn } from "@/lib/utils";
import type { SigningCoverageSummary as Summary } from "@/lib/api/signing-coverage";

// Beacon — SigningCoverageSummary. Four stat cards above the coverage table.
// The "enforced w/ empty allowlist" card uses a warning tone because it is the
// posture soft spot (require_signature on, but ANY signature passes).

interface StatCardProps {
  label: string;
  value: string;
  tone?: "default" | "warning";
}

function StatCard({ label, value, tone = "default" }: StatCardProps): React.ReactElement {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="text-xs font-medium uppercase tracking-[0.14em] text-[var(--color-fg-subtle)]">
        {label}
      </div>
      <div
        className={cn(
          "mt-2 font-display text-2xl font-semibold tabular-nums",
          tone === "warning" && "text-[var(--color-warning)]",
        )}
      >
        {value}
      </div>
    </div>
  );
}

interface SigningCoverageSummaryProps {
  summary: Summary;
}

export function SigningCoverageSummary({
  summary,
}: SigningCoverageSummaryProps): React.ReactElement {
  const pct = `${Math.round(summary.workspace_signed_tag_pct * 100)}%`;
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard
        label="Repos requiring signature"
        value={`${summary.repos_require_signature} / ${summary.repo_count}`}
      />
      <StatCard label="Workspace signed-tag coverage" value={pct} />
      <StatCard
        label="Enforced w/ empty allowlist"
        value={String(summary.repos_enforced_empty_allowlist)}
        tone={summary.repos_enforced_empty_allowlist > 0 ? "warning" : "default"}
      />
      <StatCard label="Repositories" value={String(summary.repo_count)} />
    </div>
  );
}
```

- [ ] **Step 4: Implement `signing-coverage-table.tsx`**

```tsx
import * as React from "react";
import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import type {
  AllowlistHealth,
  RepoCoverage,
} from "@/lib/api/signing-coverage";
import { SigningCoverageBar } from "./signing-coverage-bar";

// Beacon — SigningCoverageTable. Workspace rollup, one row per repo. Read-only:
// the rightmost cell drills into the existing per-repo Settings tab (trusted-key
// editor + require_signature toggle) rather than duplicating those controls.

const HEALTH_META: Record<
  AllowlistHealth,
  { label: string; className: string }
> = {
  enforced_with_allowlist: {
    label: "Enforced + allowlist",
    className: "text-[var(--color-success)] bg-[var(--color-success)]/10",
  },
  enforced_any_signature: {
    label: "Any signature",
    className: "text-[var(--color-warning)] bg-[var(--color-warning)]/10",
  },
  advisory: {
    label: "Advisory",
    className: "text-[var(--color-fg-muted)] bg-[var(--color-surface-sunken)]",
  },
};

function AllowlistHealthBadge({
  health,
  keyCount,
}: {
  health: AllowlistHealth;
  keyCount: number;
}): React.ReactElement {
  const meta = HEALTH_META[health];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium",
        meta.className,
      )}
    >
      {meta.label}
      {health !== "advisory" && (
        <span className="text-[var(--color-fg-subtle)]">· {keyCount} keys</span>
      )}
    </span>
  );
}

interface SigningCoverageTableProps {
  repos: RepoCoverage[];
}

export function SigningCoverageTable({
  repos,
}: SigningCoverageTableProps): React.ReactElement {
  const [filter, setFilter] = React.useState("");
  const [requiredOnly, setRequiredOnly] = React.useState(false);

  const rows = React.useMemo(() => {
    const q = filter.trim().toLowerCase();
    return repos.filter((r) => {
      if (requiredOnly && !r.require_signature) return false;
      if (!q) return true;
      return `${r.org}/${r.repo}`.toLowerCase().includes(q);
    });
  }, [repos, filter, requiredOnly]);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter repositories…"
          className="h-9 w-64 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm"
          aria-label="Filter repositories"
        />
        <label className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
          <input
            type="checkbox"
            checked={requiredOnly}
            onChange={(e) => setRequiredOnly(e.target.checked)}
          />
          Requires signature only
        </label>
      </div>

      <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--color-surface-sunken)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-4 py-2 font-medium">Repository</th>
              <th className="px-4 py-2 font-medium">Policy</th>
              <th className="px-4 py-2 font-medium">Signed coverage</th>
              <th className="px-4 py-2 font-medium">Trusted keys</th>
              <th className="px-4 py-2 font-medium">Recent signers</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={`${r.org}/${r.repo}`}
                className="border-t border-[var(--color-border)]"
              >
                <td className="px-4 py-2 font-medium">
                  {r.org}/{r.repo}
                </td>
                <td className="px-4 py-2">
                  {r.require_signature ? (
                    <span className="rounded bg-[var(--color-surface-sunken)] px-1.5 py-0.5 text-xs">
                      require_signature
                    </span>
                  ) : (
                    <span className="text-[var(--color-fg-subtle)]">—</span>
                  )}
                </td>
                <td className="px-4 py-2">
                  <SigningCoverageBar
                    pct={r.signed_pct}
                    signed={r.signed_tags}
                    total={r.tags_in_window}
                  />
                </td>
                <td className="px-4 py-2">
                  <AllowlistHealthBadge
                    health={r.allowlist_health}
                    keyCount={r.trusted_key_count}
                  />
                  {r.stale_trusted_keys > 0 && (
                    <span className="ml-1 text-xs text-[var(--color-fg-subtle)]">
                      ({r.stale_trusted_keys} stale)
                    </span>
                  )}
                </td>
                <td className="px-4 py-2 text-xs text-[var(--color-fg-muted)]">
                  {r.recent_signers.length === 0
                    ? "—"
                    : r.recent_signers
                        .slice(0, 3)
                        .map((s) => s.signer_id || s.key_id)
                        .join(", ")}
                </td>
                <td className="px-4 py-2 text-right">
                  <Link
                    to="/repositories/$org/$repo/settings"
                    params={{ org: r.org, repo: r.repo }}
                    className="text-xs font-medium text-[var(--color-accent)] hover:underline"
                  >
                    Settings →
                  </Link>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
```

> **Verify the drill-in route:** confirm the per-repo Settings route id and params in the generated route tree (search `routes/` for the repo-settings file — likely `_authenticated.repositories.$org.$repo.settings.tsx`). Adjust the `<Link to=... params=...>` to match the actual route id. If the route uses a single `$repo` catch-all or a different param name, mirror what `repo-signature-policy-section.tsx`'s page uses.

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd frontend && npx vitest run src/components/security/__tests__/signing-coverage-table.test.tsx`
Expected: PASS. (If the `<Link>` wrapper fails, apply the router note from Step 1.)

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/security/signing-coverage-summary.tsx \
        frontend/src/components/security/signing-coverage-table.tsx \
        frontend/src/components/security/__tests__/signing-coverage-table.test.tsx
git commit -m "feat(frontend): signing coverage summary + table"
```

---

## Task 5: Wire the Security → Signing route

**Files:**
- Modify: `frontend/src/routes/_authenticated.security.signing.tsx`
- Test: `frontend/src/routes/__tests__/security-signing.test.tsx` (create; check whether route tests live under `routes/__tests__` — if the repo tests routes elsewhere, follow that location)

- [ ] **Step 1: Write the failing test**

```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";

// Mock the data hook so the route renders deterministically.
vi.mock("@/lib/api/signing-coverage", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/signing-coverage")>(
    "@/lib/api/signing-coverage",
  );
  return {
    ...actual,
    useSigningCoverage: vi.fn(),
  };
});

import { useSigningCoverage } from "@/lib/api/signing-coverage";
import { SigningTab } from "../_authenticated.security.signing";

const mockHook = vi.mocked(useSigningCoverage);

describe("SigningTab", () => {
  it("shows the 'signing not wired' state when signer is disabled", () => {
    mockHook.mockReturnValue({
      data: { window: 50, signer_enabled: false, summary: {} as never, repos: [] },
      isLoading: false,
      isError: false,
    } as never);
    render(<SigningTab />);
    expect(screen.getByText(/signing is not wired/i)).toBeInTheDocument();
  });

  it("renders the summary + table when coverage is present", () => {
    mockHook.mockReturnValue({
      data: {
        window: 50,
        signer_enabled: true,
        summary: {
          repo_count: 1,
          repos_require_signature: 1,
          repos_enforced_empty_allowlist: 0,
          workspace_signed_tag_pct: 0.95,
        },
        repos: [
          {
            org: "acme",
            repo: "api",
            require_signature: true,
            window: 50,
            tags_in_window: 40,
            signed_tags: 38,
            signed_pct: 0.95,
            trusted_key_count: 2,
            allowlist_health: "enforced_with_allowlist",
            stale_trusted_keys: 0,
            recent_signers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
    } as never);
    render(<SigningTab />);
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText(/most-recent tags/i)).toBeInTheDocument();
  });
});
```

> The test imports `SigningTab` as a named export — Step 3 exports the component (in addition to the route). The `<Link>` in the table may again require a router wrapper; reuse the same approach chosen in Task 4.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend && npx vitest run src/routes/__tests__/security-signing.test.tsx`
Expected: FAIL — `SigningTab` is not exported / still renders the placeholder.

- [ ] **Step 3: Replace the route body**

Replace the entire contents of `frontend/src/routes/_authenticated.security.signing.tsx` with:

```tsx
// REDESIGN-001 Phase 4.2.e — Security › Signing tab.
//
// Workspace-wide Cosign signing coverage rollup (futures.md "Signing coverage
// rollup"). Per-repo signed-tag %, recent signers, trusted-key allowlist
// health, and require_signature status, from the BFF
// GET /api/v1/signing/coverage. Per-tag verify + the per-repo trusted-key
// editor live on the repository pages; this tab is the read-only rollup and
// drills into per-repo Settings for any change.
import * as React from "react";
import { createFileRoute } from "@tanstack/react-router";
import { FileSignature } from "lucide-react";
import { useSigningCoverage } from "@/lib/api/signing-coverage";
import { SigningCoverageSummary } from "@/components/security/signing-coverage-summary";
import { SigningCoverageTable } from "@/components/security/signing-coverage-table";

export const Route = createFileRoute("/_authenticated/security/signing")({
  component: SigningTab,
});

const WINDOW = 50;

export function SigningTab(): React.ReactElement {
  const { data, isLoading, isError } = useSigningCoverage(WINDOW);

  if (isLoading) {
    return (
      <p className="text-sm text-[var(--color-fg-muted)]">Loading signing coverage…</p>
    );
  }

  if (isError || !data) {
    return (
      <p className="text-sm text-[var(--color-danger)]">
        Failed to load signing coverage. Retry shortly.
      </p>
    );
  }

  // Signer not wired → the whole rollup is moot. Reuse the dashed "not wired"
  // card vocabulary the placeholder established.
  if (!data.signer_enabled) {
    return (
      <section className="rounded-lg border border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)] p-6 text-center">
        <div className="mx-auto inline-flex size-10 items-center justify-center rounded-md bg-[var(--color-surface)] text-[var(--color-fg-muted)]">
          <FileSignature className="size-5" />
        </div>
        <h2 className="mt-3 font-display text-lg font-medium">Image signing coverage</h2>
        <p className="mx-auto mt-2 max-w-prose text-sm text-[var(--color-fg-muted)]">
          Signing is not wired on this deployment (no signer service configured),
          so there is no coverage to report. Configure the signer service to see
          per-repo signed-tag coverage, recent signers, and allowlist health here.
        </p>
      </section>
    );
  }

  if (data.repos.length === 0) {
    return (
      <p className="text-sm text-[var(--color-fg-muted)]">
        No repositories yet. Coverage appears once repositories exist.
      </p>
    );
  }

  return (
    <div className="space-y-5">
      <SigningCoverageSummary summary={data.summary} />
      <SigningCoverageTable repos={data.repos} />
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Coverage computed over the {data.window} most-recent tags per repository.
      </p>
    </div>
  );
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd frontend && npx vitest run src/routes/__tests__/security-signing.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/routes/_authenticated.security.signing.tsx frontend/src/routes/__tests__/security-signing.test.tsx
git commit -m "feat(frontend): live Security → Signing coverage tab"
```

---

## Task 6: Full CI gates + docs + tracker hygiene

**Files:**
- Modify: `docs/SIGNING.md`, `README.md`, `futures.md`, `status.md`, `status-tracker.md`

- [ ] **Step 1: Run all four frontend CI gates** (CLAUDE.md §15.1)

Run:
```bash
cd frontend
npm run lint
npm run typecheck
npm run test
npm run build
```
Expected: all four green. Fix any issue — including lint errors in code the diff touched. The `pre{lint,typecheck,test}` hooks regenerate `routeTree.gen.ts`; the build must pick up the new route wiring cleanly.

- [ ] **Step 2: Run the backend service gate** (CLAUDE.md §15.2)

Run: `make -C services/management` (vet + golangci-lint + test + build), or the root `make build && make test && make lint`.
Expected: green.

- [ ] **Step 3: Document the endpoint in `docs/SIGNING.md`**

Add a "Coverage rollup" section documenting:
- `GET /api/v1/signing/coverage?window=50` — reader-allowed, workspace-scoped.
- Response shape (copy the JSON from the design spec §3).
- The `allowlist_health` enum semantics: `enforced_with_allowlist` / `enforced_any_signature` (require_signature on but empty allowlist → ANY signature passes) / `advisory`.
- That coverage is windowed (N most-recent tags per repo, default 50, cap 200) and the % is not all-tags.
- Graceful degrade: `signer_enabled:false` with `SIGNER_GRPC_ADDR` unset.

- [ ] **Step 4: Update the README capability matrix**

Add a line noting the workspace-wide signing coverage rollup (Security → Signing) alongside the existing signing/admission entries.

- [ ] **Step 5: Add a distinct `futures.md` item**

Under the signing/admission area, add a new item (do NOT fold it into the deferred "Signed-image admission Phase 3" enforcement bullet):

```
### Signing coverage rollup — DONE (2026-07-16)
- **Why:** Per-tag verify + per-repo trusted-key editor existed, but no
  workspace-wide view of signing posture.
- **What shipped:** read-only BFF GET /api/v1/signing/coverage (pure
  orchestration, no proto/migration) + Security → Signing tab: per-repo
  signed-tag % over a bounded recent-tag window, recent signers, trusted-key
  allowlist health (surfacing "enforced but empty allowlist"), and
  require_signature status. Cosign-only; Notary v2 not wired.
- **Distinct from** the deferred admission Phase 3 (quorum / rotation /
  keyless), which changes admission decisions — this is visibility only.
- **Affects:** services/management, frontend, docs/SIGNING.md.
```

- [ ] **Step 6: Tracker hygiene** (CLAUDE.md §15.3)

- Prepend a row to `status.md` describing the shipped rollup.
- If `status-tracker.md` carries an OPEN entry for this work, move it into the shipped table; otherwise add the shipped row directly.

- [ ] **Step 7: Commit**

```bash
git add docs/SIGNING.md README.md futures.md status.md status-tracker.md
git commit -m "docs: signing coverage rollup — endpoint docs + tracker hygiene"
```

---

## Final verification

- [ ] **Backend:** `make -C services/management` green.
- [ ] **Frontend:** all four gates (`lint`, `typecheck`, `test`, `build`) green.
- [ ] **Manual smoke (optional, per the `verify` skill):** run the dev stack, sign a tag in one repo, toggle `require_signature` on two repos (leave one allowlist empty), load Security → Signing, and confirm: coverage %, the empty-allowlist repo shows "Any signature" (warning), the summary counts match, and a Settings drill-in navigates correctly. Rebuild frontend + management together so the new BFF route and route tree are both live (per the "rebuild full vertical" note).
- [ ] Open PR to `main` with a summary linking the spec + this plan.

---

## Self-Review notes (already reconciled)

- **Spec coverage:** signed-tag % (Task 1 `coverageForRepo` + Task 3/4 UI), recent signers (`recent_signers` + table cell), trusted-key allowlist health (`allowlist_health` enum + badge), `require_signature` status (repo field + Policy column), workspace summary (Task 1 `summarizeCoverage` + Task 4 summary), graceful degrade (Task 1 signer-nil + Task 5 not-wired card), windowed disclosure (Task 5 caption). All present.
- **Type consistency:** Go `repoCoverage`/`signingCoverageSummary` JSON tags match the TS `RepoCoverage`/`SigningCoverageSummary` field names 1:1; `recent_signers` reuses `recentSignerEntry` (`key_id`,`signer_id`,`last_signed_at`,`tag_count`) which matches the TS `CoverageSigner`. `allowlist_health` string values are identical on both sides.
- **Known verification points flagged inline:** `recentSignerEntry` field names, `RepositoryTrustedKey.GetKeyId()`, the per-repo Settings route id/params for the drill-in `<Link>`, and the router-wrapper approach for `<Link>`-containing component tests. Each has a concrete fallback.
