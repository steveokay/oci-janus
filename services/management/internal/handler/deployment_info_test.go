// Package handler tests the public deployment-info endpoint.
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// TestHandleDeploymentInfo verifies the public read-only endpoint that
// the FE consumes at /api/v1/deployment-info. Unauthenticated by design —
// it returns only the deployment posture, not tenant data.
func TestHandleDeploymentInfo(t *testing.T) {
	h := &Handler{
		deploymentMode: loader.DeploymentModeSingle,
		buildVersion:   "test-1.0",
	}

	req := httptest.NewRequest("GET", "/api/v1/deployment-info", nil)
	rr := httptest.NewRecorder()
	h.handleDeploymentInfo(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body struct {
		Mode    string `json:"deployment_mode"`
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "single", body.Mode)
	require.Equal(t, "test-1.0", body.Version)
}

// TestHandleDeploymentInfo_MultiMode verifies that multi mode is correctly
// returned in the response.
func TestHandleDeploymentInfo_MultiMode(t *testing.T) {
	h := &Handler{
		deploymentMode: loader.DeploymentModeMulti,
		buildVersion:   "v2.0.0",
	}

	req := httptest.NewRequest("GET", "/api/v1/deployment-info", nil)
	rr := httptest.NewRecorder()
	h.handleDeploymentInfo(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "multi", body["deployment_mode"])
	require.Equal(t, "v2.0.0", body["version"])
}
