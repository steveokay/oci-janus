// Package server_test provides unit tests for the gateway HTTP middleware and
// health-check endpoint — the only Go business logic in the gateway service.
// No real network or gRPC connections are used.
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
)

// ── /healthz endpoint tests ───────────────────────────────────────────────────

func TestHealthz_ReturnsOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHealthz_HeadRequest_ReturnsOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /healthz status = %d, want 200", rec.Code)
	}
}

// ── SecureHeaders middleware tests ───────────────────────────────────────────

// newSecureMux returns a mux wrapped with SecureHeaders middleware so tests
// verify the header injection on every response path.
func newSecureMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
	})
	return httpmiddleware.SecureHeaders(mux)
}

func TestSecureHeaders_XContentTypeOptions_Nosniff(t *testing.T) {
	handler := newSecureMux()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
}

func TestSecureHeaders_XFrameOptions_Deny(t *testing.T) {
	handler := newSecureMux()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
}

func TestSecureHeaders_XXSSProtection_Disabled(t *testing.T) {
	handler := newSecureMux()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-XSS-Protection"); got != "0" {
		t.Errorf("X-XSS-Protection = %q, want %q", got, "0")
	}
}

func TestSecureHeaders_HeadersOnHTMLResponse(t *testing.T) {
	handler := newSecureMux()
	req := httptest.NewRequest(http.MethodGet, "/html", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "0",
	}
	for h, want := range headers {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("[HTML response] %s = %q, want %q", h, got, want)
		}
	}
}

func TestSecureHeaders_HeadersOnErrorResponse(t *testing.T) {
	// Even 404 responses should carry security headers.
	handler := newSecureMux()
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options on 404 = %q, want %q", got, "nosniff")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options on 404 = %q, want %q", got, "DENY")
	}
}

func TestSecureHeaders_DoesNotOverrideExistingContentType(t *testing.T) {
	// SecureHeaders does not set Content-Type — that is up to the handler.
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	})
	handler := httpmiddleware.SecureHeaders(mux)

	req := httptest.NewRequest(http.MethodGet, "/json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestSecureHeaders_PostRequest_HeadersPresent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/post", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	handler := httpmiddleware.SecureHeaders(mux)

	req := httptest.NewRequest(http.MethodPost, "/post", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options on POST = %q, want %q", got, "DENY")
	}
}
