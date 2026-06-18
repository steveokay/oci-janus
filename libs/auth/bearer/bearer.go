// Package bearer parses RFC 7235 Bearer authentication headers.
//
// PENTEST-013: the scheme name in an Authorization header is case-insensitive
// per RFC 7235 §2.1. Hand-rolled `strings.HasPrefix(h, "Bearer ")` checks
// reject `bearer xyz` (lowercase) and `BEARER xyz` (uppercase) even though
// both are spec-correct. This helper accepts any case variation while still
// rejecting non-Bearer schemes (Basic, Digest, etc.).
package bearer

import "strings"

// Extract returns (token, true) when `authHeader` begins with a case-insensitive
// "Bearer " (or "Bearer\t") followed by a non-empty token. Otherwise returns
// ("", false). Whitespace separating the scheme and the token may be a single
// SP or HTAB per the ABNF; folded whitespace is not accepted.
func Extract(authHeader string) (string, bool) {
	const scheme = "Bearer"
	if len(authHeader) <= len(scheme) {
		return "", false
	}
	if !strings.EqualFold(authHeader[:len(scheme)], scheme) {
		return "", false
	}
	sep := authHeader[len(scheme)]
	if sep != ' ' && sep != '\t' {
		return "", false
	}
	token := strings.TrimLeft(authHeader[len(scheme)+1:], " \t")
	if token == "" {
		return "", false
	}
	return token, true
}
