// Package handler tests the public deployment-info endpoint.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHandleDeploymentInfo verifies the public read-only endpoint the FE
// consumes at /api/v1/deployment-info. Unauthenticated by design — it returns
// only the build version. The platform is single-tenant only (ADR-0031), so
// the historical `deployment_mode` field was removed.
func TestHandleDeploymentInfo(t *testing.T) {
	h := &Handler{buildVersion: "test-1.0"}

	req := httptest.NewRequest("GET", "/api/v1/deployment-info", nil)
	rr := httptest.NewRecorder()
	h.handleDeploymentInfo(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "test-1.0", body["version"])

	// The deployment_mode field was removed — assert it's gone so a regression
	// that re-introduces multi-tenant chrome gating is caught here.
	_, hasMode := body["deployment_mode"]
	require.False(t, hasMode, "deployment_mode must not be present (single-tenant only)")
}
