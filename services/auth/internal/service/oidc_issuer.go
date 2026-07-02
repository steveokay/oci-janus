// Package service — oidc_issuer.go is the deploy-time issuer allowlist
// for FUT-001 federated workload identity.
//
// Operators set OIDC_ALLOWED_ISSUERS as a comma-separated list of trusted
// OIDC issuer URL prefixes (typically the official IdP root, e.g.
// `https://token.actions.githubusercontent.com`). Both trust-create AND
// token-exchange consult the same allowlist — an issuer removed from the
// env after a trust was created STOPS minting on the next exchange even
// without a DB change.
//
// The allowlist is intentionally a prefix match (not exact) so a single
// entry can cover an IdP's per-installation URLs (e.g. GitLab's
// `https://gitlab.com/group/project` issuer variants).
//
// SECURITY: an empty allowlist rejects EVERYTHING (fail-closed default).
// Self-hosters MUST name the IdPs they trust at deploy time; we do not
// ship a "trust the world" default.
package service

import "strings"

// issuerAllowed reports whether `issuer` is a boundary-safe prefix-match
// of ANY entry in `allow`. Comparison is byte-identical (no case folding —
// the OIDC spec is case-sensitive on issuer URLs).
//
// **SEC-057 (2026-07-01):** the initial implementation was a bare
// `strings.HasPrefix`, which allows `iss=https://token.actions.githubusercontent.com.evil.com`
// to match an allowlist entry of `https://token.actions.githubusercontent.com`.
// To close the subdomain-lookalike bypass, we require the character
// immediately AFTER the matched prefix to be `/` or end-of-string.
// This makes the match a "prefix that ends on a URL-path boundary" —
// legitimate issuers with per-installation paths (e.g. GitLab's
// `https://gitlab.com/group/project`) still match a shorter prefix,
// but a hostile suffix that extends the hostname does not.
//
// Empty `allow` means NOTHING is allowed (fail-closed default).
func issuerAllowed(allow []string, issuer string) bool {
	if issuer == "" {
		// An empty issuer claim cannot match any non-empty allowlist
		// entry. We special-case this so a config that includes an
		// empty prefix (e.g. from a stray trailing comma) does not
		// accidentally allow every issuer.
		return false
	}
	for _, prefix := range allow {
		if prefix == "" {
			continue
		}
		if !strings.HasPrefix(issuer, prefix) {
			continue
		}
		// SEC-057: boundary check — reject `.evil.com` suffix extension.
		// If the allowlist entry itself ends in `/`, the prefix already
		// terminates the hostname, so any character (including nothing)
		// after it is safe. Otherwise the next character must be `/` or
		// the strings must be equal length.
		if strings.HasSuffix(prefix, "/") {
			return true
		}
		if len(issuer) == len(prefix) {
			return true
		}
		if issuer[len(prefix)] == '/' {
			return true
		}
	}
	return false
}

// ParseIssuerAllowlist is the exported alias of parseIssuerAllowlist.
// Server startup uses this to translate the OIDC_ALLOWED_ISSUERS env
// CSV into a slice the constructor can consume.
func ParseIssuerAllowlist(csv string) []string {
	return parseIssuerAllowlist(csv)
}

// parseIssuerAllowlist splits a comma-separated env value into a list of
// trusted issuer prefixes. Whitespace-only entries are dropped; the rest
// are returned in original order. Used by the Service constructor and
// tested independently of issuerAllowed.
func parseIssuerAllowlist(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// SEC-057: drop entries without an explicit http(s):// scheme.
		// A bare host like `token.actions.githubusercontent.com` is almost
		// certainly a config typo — and because every real issuer_url is
		// `https://…` (enforced at trust-create), a scheme-less prefix
		// could never match anyway, so it is pure dead weight. Dropping it
		// is fail-closed: a mistyped entry simply won't authorise its
		// issuer, surfacing as a clean "not in OIDC_ALLOWED_ISSUERS" at
		// trust-create rather than silently widening the trust boundary.
		if !strings.HasPrefix(p, "https://") && !strings.HasPrefix(p, "http://") {
			continue
		}
		out = append(out, p)
	}
	return out
}
