// Package httpmiddleware provides HTTP middleware for the registry platform.
// Middleware in this package is shared across all registry services and
// applied at server construction time (see each service's server.go).
package httpmiddleware

import "net/http"

// SecureHeaders is a middleware that adds security response headers required
// by CLAUDE.md §17 to every HTTP response. It must be the outermost wrapper
// so that even error responses (e.g. from MaxBytesHandler) carry these headers.
//
// Headers set:
//   - X-Content-Type-Options: nosniff   — prevents MIME-type sniffing attacks
//   - X-Frame-Options: DENY             — prevents clickjacking via iframes
//   - X-XSS-Protection: 0              — disables legacy XSS filter; modern browsers
//     use CSP instead and the filter itself has known bypass vulnerabilities
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent browsers from guessing the content-type when it is not set
		// explicitly — a common vector for content-type confusion attacks.
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Deny embedding this service's responses inside any frame or iframe,
		// mitigating clickjacking attacks.
		w.Header().Set("X-Frame-Options", "DENY")

		// Explicitly disable the browser's built-in XSS filter. Modern browsers
		// rely on Content-Security-Policy; the legacy filter introduces its own
		// bypass vulnerabilities when enabled.
		w.Header().Set("X-XSS-Protection", "0")

		next.ServeHTTP(w, r)
	})
}
