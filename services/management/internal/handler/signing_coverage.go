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
