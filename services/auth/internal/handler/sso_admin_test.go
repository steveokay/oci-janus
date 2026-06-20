// FE-API-034 — Admin CRUD tests for SSO provider configuration.
//
// Tests stand up the SSO test server (same helper as sso_test.go) and drive
// the /api/v1/admin/auth-providers routes through HTTP. The fake user repo
// flags the caller as a tenant admin so the requireProviderAdmin gate is
// satisfied; a separate test asserts that non-admins are rejected with 403.
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ssoAdminCtx bundles a running httptest.Server plus the in-memory fakes
// the tests poke at directly. Built per-test so each test sees a fresh
// state.
type ssoAdminCtx struct {
	httpURL  string
	sso      *service.SSO
	tc       *testCtx
	tenantID uuid.UUID
}

func buildSSOAdminCtx(t *testing.T) *ssoAdminCtx {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	sso, err := service.NewSSO(tc.svc, providers, sessions, key)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	tenantID := uuid.New()

	mux := http.NewServeMux()
	h := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "http://test")
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &ssoAdminCtx{httpURL: srv.URL, sso: sso, tc: tc, tenantID: tenantID}
}

// issueAdminToken creates an admin user in the in-memory store and returns
// a JWT bound to them. The handlerFakeUserRepo flags adminUsers so
// callerIsTenantAdmin returns true.
func (c *ssoAdminCtx) issueAdminToken(t *testing.T) (token string, userID uuid.UUID) {
	t.Helper()
	userID = uuid.New()
	c.tc.users.adminUsers[userID] = true
	u := &repository.User{
		ID:       userID,
		TenantID: c.tenantID,
		Username: "admin-" + userID.String()[:8],
		Email:    userID.String()[:8] + "@example.com",
		IsActive: true,
	}
	c.tc.users.users[u.Username] = u
	tok, err := c.tc.svc.IssueToken(t.Context(), userID.String(), c.tenantID.String(), nil, []string{"admin"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok, userID
}

func (c *ssoAdminCtx) issueReaderToken(t *testing.T) string {
	t.Helper()
	userID := uuid.New()
	u := &repository.User{
		ID:       userID,
		TenantID: c.tenantID,
		Username: "reader-" + userID.String()[:8],
		Email:    "r" + userID.String()[:8] + "@example.com",
		IsActive: true,
	}
	c.tc.users.users[u.Username] = u
	tok, err := c.tc.svc.IssueToken(t.Context(), userID.String(), c.tenantID.String(), nil, []string{"reader"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// doJSON issues an HTTP request with optional bearer token and JSON body.
func doJSON(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		br = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, br)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestSSOAdmin_CreateRequiresAdmin(t *testing.T) {
	c := buildSSOAdminCtx(t)
	tok := c.issueReaderToken(t)
	resp := doJSON(t, http.MethodPost, c.httpURL+"/api/v1/admin/auth-providers", tok, map[string]any{
		"type":                "oauth_google",
		"display_name":        "Google",
		"oauth_client_id":     "cid",
		"oauth_client_secret": "csec",
		"default_role":        "reader",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 403, got %d; body=%s", resp.StatusCode, string(body))
	}
}

func TestSSOAdmin_CreateAndListNeverLeaksSecret(t *testing.T) {
	c := buildSSOAdminCtx(t)
	tok, _ := c.issueAdminToken(t)

	create := doJSON(t, http.MethodPost, c.httpURL+"/api/v1/admin/auth-providers", tok, map[string]any{
		"type":                "oauth_google",
		"display_name":        "Google",
		"oauth_client_id":     "cid",
		"oauth_client_secret": "super-secret-do-not-leak",
		"default_role":        "reader",
	})
	if create.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(create.Body)
		_ = create.Body.Close()
		t.Fatalf("create: want 201, got %d; body=%s", create.StatusCode, string(body))
	}
	createBody, _ := io.ReadAll(create.Body)
	_ = create.Body.Close()
	if bytes.Contains(createBody, []byte("super-secret-do-not-leak")) {
		t.Errorf("create response leaked plaintext secret: %s", string(createBody))
	}
	for _, banned := range []string{"oauth_client_secret", "oauth_client_secret_enc", "client_secret"} {
		var m map[string]any
		_ = json.Unmarshal(createBody, &m)
		if _, ok := m[banned]; ok {
			t.Errorf("create response leaked %s field", banned)
		}
	}

	list := doJSON(t, http.MethodGet, c.httpURL+"/api/v1/admin/auth-providers", tok, nil)
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(list.Body)
		t.Fatalf("list: want 200, got %d; body=%s", list.StatusCode, string(body))
	}
	listBody, _ := io.ReadAll(list.Body)
	if bytes.Contains(listBody, []byte("super-secret-do-not-leak")) {
		t.Errorf("list response leaked plaintext secret: %s", string(listBody))
	}
}

func TestSSOAdmin_CreateRejectsBadType(t *testing.T) {
	c := buildSSOAdminCtx(t)
	tok, _ := c.issueAdminToken(t)
	resp := doJSON(t, http.MethodPost, c.httpURL+"/api/v1/admin/auth-providers", tok, map[string]any{
		"type":         "ldap",
		"display_name": "Bad",
		"default_role": "reader",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 400, got %d; body=%s", resp.StatusCode, string(body))
	}
}

func TestSSOAdmin_CreateSAMLRequiresMetadata(t *testing.T) {
	c := buildSSOAdminCtx(t)
	tok, _ := c.issueAdminToken(t)
	resp := doJSON(t, http.MethodPost, c.httpURL+"/api/v1/admin/auth-providers", tok, map[string]any{
		"type":         "saml",
		"display_name": "Corp SAML",
		"default_role": "reader",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 400, got %d; body=%s", resp.StatusCode, string(body))
	}
}

func TestSSOAdmin_DeleteHappyPath(t *testing.T) {
	c := buildSSOAdminCtx(t)
	tok, _ := c.issueAdminToken(t)
	create := doJSON(t, http.MethodPost, c.httpURL+"/api/v1/admin/auth-providers", tok, map[string]any{
		"type":                "oauth_google",
		"display_name":        "Google",
		"oauth_client_id":     "cid",
		"oauth_client_secret": "csec",
		"default_role":        "reader",
	})
	var p adminProviderResponse
	if err := json.NewDecoder(create.Body).Decode(&p); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	_ = create.Body.Close()

	resp := doJSON(t, http.MethodDelete, c.httpURL+"/api/v1/admin/auth-providers/"+p.ID, tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("delete: want 204, got %d; body=%s", resp.StatusCode, string(body))
	}
}
