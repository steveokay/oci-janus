package middleware

import (
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

// CORS sets the Access-Control-Allow-Origin header to the explicitly configured
// origin. Never uses "*" — see CLAUDE.md §17 (FE-SEC-004).
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Security headers required on every response — CLAUDE.md §17.
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")

			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			// Enumerate every method registered in handler.Register — do not use *.
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
