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

// issuerAllowed reports whether `issuer` is a prefix-match of ANY entry
// in `allow`. Comparison is byte-identical (no case folding — the OIDC
// spec is case-sensitive on issuer URLs).
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
		if strings.HasPrefix(issuer, prefix) {
			return true
		}
	}
	return false
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
		out = append(out, p)
	}
	return out
}
