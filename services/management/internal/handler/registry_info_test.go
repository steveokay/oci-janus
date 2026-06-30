package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegistryInfo covers the FUT-002 GET /api/v1/registry-info endpoint.
// The endpoint is auth-gated at the mux layer (handler.go); these tests
// invoke the handler function directly to exercise the response shape +
// the dev-misconfig (empty PLATFORM_HOST) failure path.
func TestRegistryInfo_ReturnsConfiguredHost(t *testing.T) {
	h := New(nil, nil, nil, nil, "")
	h = h.WithPlatformHost("registry.example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/registry-info", nil)
	rec := httptest.NewRecorder()

	h.handleRegistryInfo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		RegistryHost   string `json:"registry_host"`
		SupportsOCIV11 bool   `json:"supports_oci_v1_1"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RegistryHost != "registry.example.com" {
		t.Errorf("registry_host = %q, want %q", body.RegistryHost, "registry.example.com")
	}
	if !body.SupportsOCIV11 {
		t.Errorf("supports_oci_v1_1 = false, want true")
	}
}

// TestRegistryInfo_EmptyHostReturns500 covers the dev-misconfig case where
// PLATFORM_HOST wasn't set. We fail loud rather than returning an empty
// string the FE would then render as "docker login   " (two spaces).
func TestRegistryInfo_EmptyHostReturns500(t *testing.T) {
	h := New(nil, nil, nil, nil, "")
	// Note: WithPlatformHost NOT called.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/registry-info", nil)
	rec := httptest.NewRecorder()

	h.handleRegistryInfo(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when PLATFORM_HOST unset", rec.Code)
	}
}
