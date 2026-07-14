package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// CORS returns a middleware that emits CORS headers only when the request
// carries an Origin that matches one of the configured allowed origins.
//
// `allowedOrigins` is a comma-separated list (e.g. "https://a.example,https://b.example").
// For backwards compatibility a single origin without commas still works.
//
// PENTEST-008 (2026-06-18): the previous implementation echoed a fixed origin
// on every response regardless of the request's Origin header. That weakened
// defense-in-depth (cache poisoning when intermediaries don't key on Origin)
// and made multi-origin support impossible. We now:
//   - Set `Vary: Origin` on every response so caches key on Origin
//   - Echo the request Origin in `Access-Control-Allow-Origin` ONLY when it
//     matches the allowlist; otherwise omit the header (browser blocks)
//   - Emit other CORS headers only when an allowed Origin is present, so
//     non-CORS responses stay clean
//
// The CLAUDE.md §17 security headers (X-Content-Type-Options, X-Frame-Options,
// X-XSS-Protection) are NOT set here — they are owned by the shared
// httpmiddleware.SecureHeaders wrapper that sits outside this middleware (see
// server.buildHandler), so they cover CORS preflight 204s and auth 401s too.
func CORS(allowedOrigins string) func(http.Handler) http.Handler {
	allowlist := parseOrigins(allowedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Vary: Origin so caches don't serve a CORS response for one origin
			// in response to a request from a different origin.
			w.Header().Set("Vary", "Origin")

			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin, allowlist) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				// Expose-Headers lets browser callers read custom response
				// headers the BFF sets — without this, browsers strip them
				// from the response that fetch()/axios sees. X-Janus-Warning
				// surfaces side-channel state like "you just deleted the
				// primary domain, workspace fell back to the platform host."
				w.Header().Set("Access-Control-Expose-Headers", "X-Janus-Warning")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			// Preflight: respond 204 regardless of allowlist outcome — if the
			// origin is not allowed, the browser blocks at the missing
			// Access-Control-Allow-Origin header (this is the spec-correct
			// behaviour; replying 403 to a preflight would leak the allowlist).
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// parseOrigins splits a comma-separated allowlist into a normalised slice.
// Empty entries and surrounding whitespace are stripped.
func parseOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// originAllowed performs an exact (case-sensitive per RFC 6454) origin match.
// We deliberately do not support `*` or wildcard subdomain matching to keep
// the policy explicit.
func originAllowed(origin string, allowlist []string) bool {
	for _, a := range allowlist {
		if a == origin {
			return true
		}
	}
	return false
}

// reRequestID matches safe X-Request-ID values: alphanumeric, hyphens, underscores, max 64 chars.
// Values outside this set are replaced with a freshly generated UUID to prevent
// log injection or response-header injection via CRLF sequences.
var reRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// RequestID ensures every response carries an X-Request-ID header for tracing.
// Passes through a client-supplied value only if it matches the safe pattern;
// otherwise generates a new UUID. This prevents log/header injection.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !reRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}
