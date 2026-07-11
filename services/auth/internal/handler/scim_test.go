package handler

import (
	"encoding/json"
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

// newSCIMDiscoveryTestHandler registers the discovery routes under
// requireSCIMAuth with an always-true verifier. Discovery handlers are methods
// on *HTTPHandler but read no service state, so a zero-value handler suffices.
func newSCIMDiscoveryTestHandler(_ *testing.T) http.Handler {
	mux := http.NewServeMux()
	h := &HTTPHandler{}
	verify := func(string) (bool, error) { return true, nil }
	g := func(fn http.HandlerFunc) http.HandlerFunc { return requireSCIMAuth(verify, fn) }
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", g(h.scimServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", g(h.scimResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", g(h.scimSchemas))
	return mux
}

func TestSCIMDiscovery_serviceProviderConfig(t *testing.T) {
	h := newSCIMDiscoveryTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["patch"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise a patch capability")
	}
	if _, ok := body["authenticationSchemes"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise authenticationSchemes")
	}
}
