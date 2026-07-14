package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBuildHandler_SetsSecurityHeaders is the SF-1 (SEC-089) regression guard:
// every response from the management BFF — including short-circuit 401s/404s
// that never reach a handler — must carry the CLAUDE.md §17 security headers.
// Before this, the chain omitted SecureHeaders entirely, so no management
// response set X-Content-Type-Options: nosniff.
func TestBuildHandler_SetsSecurityHeaders(t *testing.T) {
	// Register a plain 200 handler that writes a body WITHOUT http.Error —
	// like the BFF's writeJSON success path. Go's stdlib http.Error already
	// stamps nosniff on error responses for free, so a 404 would pass
	// trivially; a success response is the real gap SecureHeaders must close.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	h := buildHandler(mux, "http://localhost:5173")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ok", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
	// X-XSS-Protection: 0 is the header management lacked before consolidating
	// onto the shared SecureHeaders helper — assert it now lands too.
	if got := rec.Header().Get("X-XSS-Protection"); got != "0" {
		t.Errorf("X-XSS-Protection = %q, want %q", got, "0")
	}
}

// TestBuildHandler_PreservesRequestID confirms wrapping with SecureHeaders did
// not drop the existing RequestID middleware (which stamps X-Request-ID).
func TestBuildHandler_PreservesRequestID(t *testing.T) {
	h := buildHandler(http.NewServeMux(), "http://localhost:5173")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header missing — RequestID middleware not in chain")
	}
}
