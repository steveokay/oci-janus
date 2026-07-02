// Package service — oidc_jwks.go is the in-process JWKS cache for the
// FUT-001 federated-workload-identity exchange flow.
//
// On every exchange we need the issuer's current public keys to verify
// the OIDC JWT signature. Re-fetching the JWKS on every request would
// add ~50ms of network latency per token and would also be a polite-DoS
// vector against the IdP's well-known endpoint. We cache per-issuer
// keys for the configurable TTL (default 3600s) and coalesce concurrent
// fetches under a mutex so a thundering herd resolves to ONE network
// round-trip.
//
// SECURITY: the cache is FAIL-CLOSED on network errors. We do NOT serve
// stale entries past their TTL — an IdP outage surfaces as
// codes.Unavailable to the CI runner (retryable) rather than silently
// trusting cached keys that may have been retired upstream.
//
// SEC-048 ANALOG: the cache enforces a 16-issuer hard cap so an
// attacker who can register many trust configs cannot drive unbounded
// memory growth. Eviction is by oldest fetchedAt so a hot issuer
// stays warm; cold issuers fall out.
package service

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxJWKSCacheSize bounds the number of distinct issuer URLs the process
// holds in memory. Mirrors SEC-048's keyring cap (16) — operators who
// genuinely need more should retire stale trusts, not raise the cap.
const maxJWKSCacheSize = 16

// jwksMaxResponseBytes caps the discovery + JWKS response bodies (SEC-059).
// A realistic JWKS with ~10 keys is ~5 KiB; 1 MiB is 200× headroom while
// preventing a hostile IdP from streaming an unbounded body to drive OOM
// within the 5s request window. Enforced inside getJSON via io.LimitReader.
const jwksMaxResponseBytes = 1 << 20 // 1 MiB

// cachedJWKS is a single issuer's keys plus the fetched-at timestamp
// used to gate TTL expiry.
type cachedJWKS struct {
	// keys maps the JWT `kid` header to the RSA public key it identifies.
	// Lookup at exchange time is O(1) per kid.
	keys map[string]*rsa.PublicKey
	// fetchedAt is the wall-clock time of the successful HTTP fetch.
	// Compared against TTL on every Fetch to decide cache hit vs refresh.
	fetchedAt time.Time
}

// jwksCache is the process-wide cache. Mutex-guarded so concurrent
// exchanges against the same issuer coalesce to one HTTP fetch.
type jwksCache struct {
	mu      sync.Mutex
	entries map[string]*cachedJWKS // keyed by issuer URL
	client  *http.Client
}

// newJWKSCache constructs a JWKS cache using the given HTTP client. The
// caller owns the client's timeout configuration — typical value is 5s
// (each fetch is one discovery GET + one JWKS GET).
func newJWKSCache(client *http.Client) *jwksCache {
	if client == nil {
		client = http.DefaultClient
	}
	return &jwksCache{
		entries: make(map[string]*cachedJWKS),
		client:  client,
	}
}

// Fetch returns the issuer's public keys keyed by `kid`. Hits the network
// on first request or after TTL expiry.
//
// **Concurrency:** the cache is guarded by a single mutex held across the
// HTTP fetch. Concurrent callers for the SAME issuer therefore serialise +
// share one HTTP round-trip (the second caller re-checks the cache after
// acquiring the mutex and finds the refreshed entry). Concurrent callers
// for DIFFERENT issuers also serialise, which is a throughput cost that
// only bites at high concurrency across many distinct issuers — the
// typical self-hoster shape (1–2 issuers total, well under the 16-issuer
// cap) makes this a non-issue in practice. See REM-023 follow-up for the
// `singleflight.Group` upgrade path if a multi-issuer deployment starves.
//
// **Fail-closed on network errors:** returns an error rather than serving
// stale entries. Callers translate the error to codes.Unavailable so the
// CI runner gets a retryable response.
func (c *jwksCache) Fetch(ctx context.Context, issuer string, ttl time.Duration) (map[string]*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[issuer]; ok {
		if time.Since(entry.fetchedAt) < ttl {
			return entry.keys, nil
		}
		// Entry exists but is expired — fall through to refresh. The
		// expired entry stays in the map until the refresh succeeds, so
		// a transient failure leaves the old entry in place for the
		// NEXT call (which will try the network again). This is a soft
		// "best-effort grace" — fail-closed remains for THIS call.
	}

	// SEC-048 analog: enforce the 16-issuer hard cap. Only evict when
	// we're adding a new entry; refreshing an existing entry doesn't
	// grow the table.
	if _, present := c.entries[issuer]; !present && len(c.entries) >= maxJWKSCacheSize {
		c.evictOldestLocked()
	}

	keys, err := c.fetchJWKS(ctx, issuer)
	if err != nil {
		return nil, err
	}
	c.entries[issuer] = &cachedJWKS{keys: keys, fetchedAt: time.Now()}
	return keys, nil
}

// evictOldestLocked drops the cache entry with the oldest fetchedAt.
// Caller MUST hold c.mu.
func (c *jwksCache) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, v := range c.entries {
		if first || v.fetchedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.fetchedAt
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// fetchJWKS resolves the issuer's `/.well-known/openid-configuration`,
// reads the `jwks_uri`, and parses the JWKS document. Non-RSA keys are
// silently skipped — we only support RS256 OIDC tokens (the dominant
// shape for GitHub Actions, GitLab CI, Buildkite).
func (c *jwksCache) fetchJWKS(ctx context.Context, issuer string) (map[string]*rsa.PublicKey, error) {
	// Step 1 — discovery. The OIDC spec says the discovery doc lives at
	// {issuer}/.well-known/openid-configuration. We do NOT trust the
	// issuer field inside the discovery doc to match our request — an
	// attacker who could MITM the well-known endpoint could redirect us
	// to a different JWKS, but they'd also need to forge the TLS cert,
	// which we delegate to crypto/tls. (The standard wraps these layers.)
	disc, err := c.getJSON(ctx, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("fetch discovery: %w", err)
	}
	jwksURI, _ := disc["jwks_uri"].(string)
	if jwksURI == "" {
		return nil, fmt.Errorf("discovery document missing jwks_uri")
	}

	// SEC-058: the discovery document is attacker-influenceable (a
	// compromised or hostile IdP controls its body), so jwks_uri is
	// untrusted input. Constrain it to https AND the SAME host as the
	// issuer before fetching — every real IdP serves its JWKS on the
	// discovery host, so any deviation (an RFC-1918 address, a cloud
	// metadata endpoint, a different public host) is an SSRF attempt.
	// Combined with the no-redirect client (NewOIDCTrustService) this
	// removes the "point us at an internal endpoint" primitive entirely.
	if err := validateJWKSURI(issuer, jwksURI); err != nil {
		return nil, err
	}

	// Step 2 — JWKS.
	raw, err := c.getJSON(ctx, jwksURI)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}

	out := make(map[string]*rsa.PublicKey)
	keys, _ := raw["keys"].([]any)
	for _, k := range keys {
		m, ok := k.(map[string]any)
		if !ok {
			continue
		}
		// We only support RSA keys for now. EC support is a follow-up
		// once a non-RSA issuer enters the trust list.
		if kty, _ := m["kty"].(string); kty != "RSA" {
			continue
		}
		kid, _ := m["kid"].(string)
		nb64, _ := m["n"].(string)
		eb64, _ := m["e"].(string)
		if kid == "" || nb64 == "" || eb64 == "" {
			continue
		}
		nbytes, err := base64.RawURLEncoding.DecodeString(nb64)
		if err != nil {
			continue
		}
		ebytes, err := base64.RawURLEncoding.DecodeString(eb64)
		if err != nil {
			continue
		}
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(nbytes),
			E: int(new(big.Int).SetBytes(ebytes).Int64()),
		}
		out[kid] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("jwks document contained no RSA keys")
	}
	return out, nil
}

// validateJWKSURI enforces that the discovery-supplied jwks_uri shares the
// issuer's ORIGIN — same scheme and same host (SEC-058). The issuer itself
// is already vetted (allowlisted before we ever fetch, and https-only for
// real trusts per SEC-063), so pinning jwks_uri to the issuer's origin
// transitively requires https in production while still permitting the
// http origins used by local-dev / test IdPs. This is stricter and more
// principled than a bare `scheme == "https"` literal: it also blocks a
// scheme *downgrade* relative to the issuer, and it rejects any exotic
// scheme (file://, gopher://, …) since those can never equal the issuer's
// http/https scheme.
//
// Host comparison is case-insensitive per RFC 3986 §3.2.2 and includes the
// port, so an attacker-controlled discovery doc cannot point jwks_uri at
// issuer-host:alternate-port, a different public host, or an RFC-1918
// address either. Combined with the no-redirect client this removes the
// SSRF primitive entirely.
func validateJWKSURI(issuer, jwksURI string) error {
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("parse issuer url: %w", err)
	}
	jwksURL, err := url.Parse(jwksURI)
	if err != nil {
		return fmt.Errorf("parse jwks_uri: %w", err)
	}
	if jwksURL.Scheme != issuerURL.Scheme {
		return fmt.Errorf("jwks_uri scheme %q does not match issuer scheme %q", jwksURL.Scheme, issuerURL.Scheme)
	}
	if !strings.EqualFold(jwksURL.Host, issuerURL.Host) {
		return fmt.Errorf("jwks_uri host %q does not match issuer host %q", jwksURL.Host, issuerURL.Host)
	}
	return nil
}

// getJSON does a GET and decodes the body as a JSON object. Used twice
// per refresh: once for discovery, once for the JWKS itself.
//
// SEC-059: the body is read through an io.LimitReader capped at
// jwksMaxResponseBytes so a hostile IdP cannot stream an unbounded body
// to exhaust memory. We read one byte past the cap and error if the
// stream is still going, so a body exactly at the limit succeeds but an
// oversize one is rejected rather than silently truncated (a truncated
// JSON doc would fail to decode anyway, but the explicit cap gives a
// clear error and stops the allocation early).
func (c *jwksCache) getJSON(ctx context.Context, rawURL string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, jwksMaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > jwksMaxResponseBytes {
		return nil, fmt.Errorf("response exceeded %d bytes", jwksMaxResponseBytes)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// size returns the number of issuers currently cached. Exposed for tests
// asserting the SEC-048 cap; also useful for an operator-visible metric
// follow-up.
func (c *jwksCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
