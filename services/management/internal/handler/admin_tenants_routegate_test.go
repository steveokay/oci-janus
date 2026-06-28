// Package handler test for REDESIGN-001 Phase 2.3 (RM-005).
//
// Pins the deployment-mode gate on the tenant-create + tenant-delete BFF
// routes. In single mode the routes must not exist on the mux at all —
// not even an authenticated platform admin can hit them. In multi mode
// they remain available so the admin Tenants page works as before.
//
// We don't exercise the full handler chain — that needs an auth backend,
// a tenant gRPC client, etc. — we just confirm the route is registered
// (or not) by asking http.ServeMux.Handler for the pattern it would
// match. An empty pattern means the mux has no route for that
// method+path; an unauthenticated request would 404. That's exactly
// what we want.
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/steveokay/oci-janus/libs/config/loader"
	"github.com/stretchr/testify/require"
)

func TestAdminTenantsRoutes_SingleMode_NoCreateOrDelete(t *testing.T) {
	mux := mustRegisterMinimalHandler(t, loader.DeploymentModeSingle)

	// POST /api/v1/admin/tenants should NOT be registered in single mode.
	postReq := httptest.NewRequest("POST", "/api/v1/admin/tenants", nil)
	_, postPattern := mux.Handler(postReq)
	require.Empty(t, postPattern, "POST /api/v1/admin/tenants must not be registered in single mode")

	// DELETE /api/v1/admin/tenants/{tenantID} same.
	delReq := httptest.NewRequest("DELETE", "/api/v1/admin/tenants/abc", nil)
	_, delPattern := mux.Handler(delReq)
	require.Empty(t, delPattern, "DELETE /api/v1/admin/tenants/{tenantID} must not be registered in single mode")

	// GET (list) and PATCH (rename) MUST stay registered — single-mode
	// operators still need to view + rename their one tenant.
	getReq := httptest.NewRequest("GET", "/api/v1/admin/tenants", nil)
	_, getPattern := mux.Handler(getReq)
	require.NotEmpty(t, getPattern, "GET /api/v1/admin/tenants must remain available in single mode")

	patchReq := httptest.NewRequest("PATCH", "/api/v1/admin/tenants/abc", nil)
	_, patchPattern := mux.Handler(patchReq)
	require.NotEmpty(t, patchPattern, "PATCH /api/v1/admin/tenants/{tenantID} must remain available in single mode")
}

func TestAdminTenantsRoutes_MultiMode_AllRoutesRegistered(t *testing.T) {
	mux := mustRegisterMinimalHandler(t, loader.DeploymentModeMulti)

	// Pin the EXACT pattern strings the mux registers, not just non-empty —
	// that way an accidental rename (e.g. `/admin/tenant`) would be caught
	// here rather than only by an integration test much further down.
	for _, tc := range []struct {
		method      string
		path        string
		wantPattern string
	}{
		{"GET", "/api/v1/admin/tenants", "GET /api/v1/admin/tenants"},
		{"POST", "/api/v1/admin/tenants", "POST /api/v1/admin/tenants"},
		{"GET", "/api/v1/admin/tenants/abc", "GET /api/v1/admin/tenants/{tenantID}"},
		{"PATCH", "/api/v1/admin/tenants/abc", "PATCH /api/v1/admin/tenants/{tenantID}"},
		{"DELETE", "/api/v1/admin/tenants/abc", "DELETE /api/v1/admin/tenants/{tenantID}"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		_, pattern := mux.Handler(req)
		require.Equal(t, tc.wantPattern, pattern, "%s %s pattern mismatch", tc.method, tc.path)
	}
}

// TestAdminTenantsRoutes_SingleMode_Returns405 closes the loop between
// "no pattern registered for this method" (the gate's mux-level invariant)
// and "client sees an error" (what the operator actually experiences).
//
// Note: 405, not 404. GET and PATCH on the same paths remain registered
// in single mode, so net/http's ServeMux returns Method Not Allowed
// (with an Allow: header listing the registered methods) rather than a
// plain 404. That's still strictly better than returning a handler-level
// 403/JSON response: the body never confirms platform-admin gating
// existed and the response shape is indistinguishable from any other
// route that doesn't support POST/DELETE.
func TestAdminTenantsRoutes_SingleMode_Returns405(t *testing.T) {
	mux := mustRegisterMinimalHandler(t, loader.DeploymentModeSingle)

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

// mustRegisterMinimalHandler builds a Handler with just enough wiring to
// call Register(mux). We don't need the auth client to behave correctly —
// the gate happens at route-registration time, not at request time, so
// the auth middleware can be nil for this assertion.
func mustRegisterMinimalHandler(t *testing.T, mode loader.DeploymentMode) *http.ServeMux {
	t.Helper()
	h := &Handler{deploymentMode: mode}
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
