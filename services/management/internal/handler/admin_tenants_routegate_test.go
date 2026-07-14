// Package handler test — admin-tenant route registration (REDESIGN-001
// Phase 9.2 / ADR-0031).
//
// The platform is single-tenant only, so the BFF must never expose tenant
// create/delete. POST /api/v1/admin/tenants and DELETE
// /api/v1/admin/tenants/{tenantID} must not be registered at all; GET (list +
// detail) and PATCH (rename) stay so the one tenant can be inspected + renamed.
//
// We don't exercise the full handler chain — we just confirm the route is
// registered (or not) by asking http.ServeMux.Handler for the pattern it would
// match. An empty pattern means the mux has no route for that method+path.
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminTenantsRoutes_NoCreateOrDelete(t *testing.T) {
	mux := mustRegisterMinimalHandler(t)

	// POST /api/v1/admin/tenants must NOT be registered — no tenant creation.
	postReq := httptest.NewRequest("POST", "/api/v1/admin/tenants", nil)
	_, postPattern := mux.Handler(postReq)
	require.Empty(t, postPattern, "POST /api/v1/admin/tenants must not be registered (single-tenant)")

	// DELETE /api/v1/admin/tenants/{tenantID} same.
	delReq := httptest.NewRequest("DELETE", "/api/v1/admin/tenants/abc", nil)
	_, delPattern := mux.Handler(delReq)
	require.Empty(t, delPattern, "DELETE /api/v1/admin/tenants/{tenantID} must not be registered (single-tenant)")

	// GET (list + detail) and PATCH (rename) MUST stay registered — pin the
	// exact patterns so an accidental rename is caught here.
	for _, tc := range []struct {
		method      string
		path        string
		wantPattern string
	}{
		{"GET", "/api/v1/admin/tenants", "GET /api/v1/admin/tenants"},
		{"GET", "/api/v1/admin/tenants/abc", "GET /api/v1/admin/tenants/{tenantID}"},
		{"PATCH", "/api/v1/admin/tenants/abc", "PATCH /api/v1/admin/tenants/{tenantID}"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		_, pattern := mux.Handler(req)
		require.Equal(t, tc.wantPattern, pattern, "%s %s pattern mismatch", tc.method, tc.path)
	}
}

// TestAdminTenantsRoutes_Returns405 closes the loop between "no pattern
// registered for this method" and "client sees an error". Because GET/PATCH
// share the tenant paths, an unregistered POST/DELETE surfaces as 405 Method
// Not Allowed (not a plain 404), and the Allow header must not advertise the
// gated method.
func TestAdminTenantsRoutes_Returns405(t *testing.T) {
	mux := mustRegisterMinimalHandler(t)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/admin/tenants"},
		{"DELETE", "/api/v1/admin/tenants/abc"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusMethodNotAllowed, rr.Code,
			"%s %s expected 405", tc.method, tc.path)
		// Allow header must NOT advertise the gated method.
		allow := rr.Header().Get("Allow")
		require.NotContains(t, allow, tc.method,
			"%s %s Allow header must not advertise the gated method (got %q)",
			tc.method, tc.path, allow)
	}
}

// mustRegisterMinimalHandler builds a Handler with just enough wiring to call
// Register(mux). The route set is static (no deployment-mode branch), so a
// zero-value Handler is sufficient for the registration assertions.
func mustRegisterMinimalHandler(t *testing.T) *http.ServeMux {
	t.Helper()
	h := &Handler{}
	mux := http.NewServeMux()
	defer func() {
		// Register touches many other route registrations; if any depend
		// on a non-nil client we'll panic. Fail the test with a clearer
		// message than the bare panic.
		if r := recover(); r != nil {
			t.Fatalf("Register panicked — minimal Handler is missing a dependency: %v", r)
		}
	}()
	h.Register(mux)
	return mux
}
