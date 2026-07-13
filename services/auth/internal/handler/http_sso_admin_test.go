package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// FE-API-034 — SSO admin HTTP routes.
//
// The routes are global-admin-gated and back the dashboard's SSO config panel.
// We exercise the auth gate + the CRUD round-trip end-to-end through httptest.

// buildSSOAdminServer stands up the auth handler (with the SSO sub-service on
// in-memory fakes) and returns the running server plus the test context so a
// test can register users / mint tokens against the same *Service.
func buildSSOAdminServer(t *testing.T) (*httptest.Server, *testCtx, uuid.UUID) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, globalProviders, sessions, key)
	require.NoError(t, err)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, tc, tenantID
}

func ssoAdminReq(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

func globalAdminToken(t *testing.T, tc *testCtx, tenantID uuid.UUID) string {
	t.Helper()
	id := registerTestUser(t, tc.svc, tenantID, "gadmin", "Str0ng!Password123")
	require.NoError(t, tc.users.SetGlobalAdmin(context.Background(), id, true))
	return issueTestToken(t, tc.svc, id.String(), tenantID.String(), nil)
}

func TestSSOAdmin_requiresGlobalAdmin(t *testing.T) {
	srv, tc, tenantID := buildSSOAdminServer(t)
	url := srv.URL + "/api/v1/auth/admin/providers"

	// No token → 401.
	resp, _ := ssoAdminReq(t, http.MethodGet, url, "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// A non-admin user → 403.
	uid := registerTestUser(t, tc.svc, tenantID, "regular", "Str0ng!Password123")
	userTok := issueTestToken(t, tc.svc, uid.String(), tenantID.String(), nil)
	resp, _ = ssoAdminReq(t, http.MethodGet, url, userTok, nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Non-admin can't write either.
	resp, _ = ssoAdminReq(t, http.MethodPut, url+"/github", userTok, map[string]any{
		"kind": "oauth_github", "display_name": "GitHub", "oauth_client_id": "c", "client_secret": "s",
	})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestSSOAdmin_upsertListRoundTrip(t *testing.T) {
	srv, tc, tenantID := buildSSOAdminServer(t)
	tok := globalAdminToken(t, tc, tenantID)
	base := srv.URL + "/api/v1/auth/admin/providers"

	// Create.
	resp, data := ssoAdminReq(t, http.MethodPut, base+"/github", tok, map[string]any{
		"kind": "oauth_github", "display_name": "GitHub", "enabled": true,
		"oauth_client_id": "client-abc", "client_secret": "s3cr3t", "auto_provision": true,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", data)

	var item map[string]any
	require.NoError(t, json.Unmarshal(data, &item))
	require.Equal(t, "github", item["id"])
	require.Equal(t, true, item["has_secret"])
	// The plaintext secret must never appear in the response.
	require.NotContains(t, string(data), "s3cr3t")

	// List shows it with has_secret and no secret value.
	resp, data = ssoAdminReq(t, http.MethodGet, base, tok, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(data), "\"github\"")
	require.Contains(t, string(data), "\"has_secret\":true")
	require.NotContains(t, string(data), "s3cr3t")

	// Update with an empty secret + disable — has_secret stays true.
	resp, data = ssoAdminReq(t, http.MethodPut, base+"/github", tok, map[string]any{
		"kind": "oauth_github", "display_name": "GitHub Enterprise", "enabled": false,
		"oauth_client_id": "client-abc", "client_secret": "",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(data, &item))
	require.Equal(t, true, item["has_secret"])
	require.Equal(t, false, item["enabled"])
	require.Equal(t, "GitHub Enterprise", item["display_name"])
}

func TestSSOAdmin_rejectsSAMLAndBadConfig(t *testing.T) {
	srv, tc, tenantID := buildSSOAdminServer(t)
	tok := globalAdminToken(t, tc, tenantID)
	base := srv.URL + "/api/v1/auth/admin/providers"

	// SAML editing is deferred → 400.
	resp, _ := ssoAdminReq(t, http.MethodPut, base+"/okta", tok, map[string]any{
		"kind": "saml", "display_name": "Okta",
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Creating an OAuth provider without a secret → 400.
	resp, _ = ssoAdminReq(t, http.MethodPut, base+"/github", tok, map[string]any{
		"kind": "oauth_github", "display_name": "GitHub", "oauth_client_id": "c", "client_secret": "",
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestSSOAdmin_delete(t *testing.T) {
	srv, tc, tenantID := buildSSOAdminServer(t)
	tok := globalAdminToken(t, tc, tenantID)
	base := srv.URL + "/api/v1/auth/admin/providers"

	resp, _ := ssoAdminReq(t, http.MethodPut, base+"/github", tok, map[string]any{
		"kind": "oauth_github", "display_name": "GitHub", "enabled": true,
		"oauth_client_id": "c", "client_secret": "s",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = ssoAdminReq(t, http.MethodDelete, base+"/github", tok, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Deleting a missing provider → 404.
	resp, _ = ssoAdminReq(t, http.MethodDelete, base+"/github", tok, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
