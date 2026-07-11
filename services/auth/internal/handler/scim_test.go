package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newSCIMAuthTestHandler wires requireSCIMAuth(verify, next) onto a mux under
// GET /scim/v2/Users, where next writes 200. Lives in the handler package so it
// can reach the unexported requireSCIMAuth.
func newSCIMAuthTestHandler(_ *testing.T, verify scimVerifier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scim/v2/Users", requireSCIMAuth(verify, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return mux
}

func TestRequireSCIMAuth_rejectsBadToken(t *testing.T) {
	verify := func(raw string) (bool, error) { return raw == "scim.good", nil }
	h := newSCIMAuthTestHandler(t, verify)

	// No/blank Authorization → 401.
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}

	// Right token → reaches next (200).
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good token: want 200, got %d", rec.Code)
	}
}
