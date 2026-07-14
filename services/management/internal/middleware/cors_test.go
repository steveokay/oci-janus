package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// noopHandler is the inner handler used by CORS tests — it succeeds without
// inspecting the request.
var noopHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestCORS_allowedOrigin_echoes verifies that an allowed Origin gets echoed
// back, plus the rest of the CORS headers, plus a Vary: Origin so caches
// don't bleed responses across origins.
func TestCORS_allowedOrigin_echoes(t *testing.T) {
	h := CORS("https://app.example,https://other.example")(noopHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, "https://app.example")
	}
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary: got %q, want %q", got, "Origin")
	}
}

// TestCORS_disallowedOrigin_omitsHeader — PENTEST-008: a request from a
// non-allowlisted origin must NOT receive Access-Control-Allow-Origin. The
// browser then blocks the cross-origin response.
func TestCORS_disallowedOrigin_omitsHeader(t *testing.T) {
	h := CORS("https://app.example")(noopHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin: got %q, want \"\" (origin must not be echoed)", got)
	}
	// Vary must still be set so a caching proxy keys on Origin even for the
	// blocked response.
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary: got %q, want %q", got, "Origin")
	}
}

// TestCORS_noOrigin_skipsCORSHeaders confirms that same-origin / non-CORS
// requests don't get CORS headers cluttering the response.
func TestCORS_noOrigin_skipsCORSHeaders(t *testing.T) {
	h := CORS("https://app.example")(noopHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Deliberately no Origin header.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin set on non-CORS request: %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods set on non-CORS request: %q", got)
	}
	// The §17 security headers are no longer owned by CORS — they moved to the
	// shared httpmiddleware.SecureHeaders wrapper (see server.buildHandler +
	// its TestBuildHandler_SetsSecurityHeaders). CORS still owns Vary: Origin.
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary: got %q, want Origin", got)
	}
}

// TestCORS_preflightAlwaysReturns204 confirms that OPTIONS requests get 204
// regardless of allowlist outcome. Returning 403 on disallowed preflight
// would leak the allowlist back to the caller.
func TestCORS_preflightAlwaysReturns204(t *testing.T) {
	h := CORS("https://app.example")(noopHandler)

	for _, origin := range []string{"https://app.example", "https://evil.example", ""} {
		t.Run("origin="+origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/", nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNoContent {
				t.Errorf("preflight status: got %d, want 204", rr.Code)
			}
		})
	}
}

// TestCORS_caseSensitiveMatch — RFC 6454 origins are case-sensitive (scheme
// + host should compare with their canonical lowercase form). The allowlist
// is an exact-string match by design; subtle case mismatches should fail
// rather than be silently accepted.
func TestCORS_caseSensitiveMatch(t *testing.T) {
	h := CORS("https://app.example")(noopHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://APP.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected case-mismatch to fail allowlist, got %q", got)
	}
}
