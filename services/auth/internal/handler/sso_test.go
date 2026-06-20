// FE-API-034 — HTTP handler tests for the SSO flow.
//
// The tests stand up an httptest.Server with the SSO sub-service wired to
// hand-written in-memory fakes (no real PostgreSQL). A second
// httptest.Server impersonates the IdP so the OAuth flow can run
// end-to-end inside one process.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── In-memory provider + session fakes ──────────────────────────────────────

type fakeProviderRepo struct {
	mu        sync.Mutex
	providers map[uuid.UUID]*repository.AuthProvider
}

func newFakeProviderRepo() *fakeProviderRepo {
	return &fakeProviderRepo{providers: make(map[uuid.UUID]*repository.AuthProvider)}
}

func (f *fakeProviderRepo) Create(_ context.Context, p *repository.AuthProvider) (*repository.AuthProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Per-canonical-type uniqueness like the SQL constraint.
	switch p.Type {
	case repository.AuthProviderOAuthGoogle, repository.AuthProviderOAuthGitHub, repository.AuthProviderOAuthMicrosoft:
		for _, existing := range f.providers {
			if existing.TenantID == p.TenantID && existing.Type == p.Type {
				return nil, repository.ErrAlreadyExists
			}
		}
	}
	cp := *p
	cp.ID = uuid.New()
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = time.Now()
	f.providers[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeProviderRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.AuthProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.providers[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	out := *p
	return &out, nil
}

func (f *fakeProviderRepo) ListByTenant(_ context.Context, tenantID uuid.UUID, enabledOnly bool) ([]*repository.AuthProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*repository.AuthProvider, 0, len(f.providers))
	for _, p := range f.providers {
		if p.TenantID != tenantID {
			continue
		}
		if enabledOnly && !p.Enabled {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeProviderRepo) Update(_ context.Context, id uuid.UUID, req repository.UpdateAuthProviderRequest) (*repository.AuthProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.providers[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if req.DisplayName != nil {
		p.DisplayName = *req.DisplayName
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.OAuthClientID != nil {
		p.OAuthClientID = *req.OAuthClientID
	}
	if req.OAuthClientSecretEnc != nil {
		p.OAuthClientSecretEnc = *req.OAuthClientSecretEnc
	}
	if req.OAuthIssuerURL != nil {
		p.OAuthIssuerURL = *req.OAuthIssuerURL
	}
	if req.OAuthScopes != nil {
		p.OAuthScopes = *req.OAuthScopes
	}
	if req.AutoProvision != nil {
		p.AutoProvision = *req.AutoProvision
	}
	if req.DefaultRole != nil {
		p.DefaultRole = *req.DefaultRole
	}
	p.UpdatedAt = time.Now()
	out := *p
	return &out, nil
}

func (f *fakeProviderRepo) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.providers[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.providers, id)
	return nil
}

type fakeSessionRepo struct {
	mu       sync.Mutex
	sessions map[string]*repository.LoginSession
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{sessions: make(map[string]*repository.LoginSession)}
}

func (f *fakeSessionRepo) Create(_ context.Context, s *repository.LoginSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.sessions[s.State]; exists {
		return repository.ErrAlreadyExists
	}
	cp := *s
	cp.CreatedAt = time.Now()
	f.sessions[s.State] = &cp
	return nil
}

func (f *fakeSessionRepo) ConsumeByState(_ context.Context, state string) (*repository.LoginSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[state]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if time.Now().After(s.ExpiresAt) {
		delete(f.sessions, state)
		return nil, repository.ErrNotFound
	}
	delete(f.sessions, state)
	out := *s
	return &out, nil
}

func (f *fakeSessionRepo) DeleteExpired(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	now := time.Now()
	for k, s := range f.sessions {
		if now.After(s.ExpiresAt) {
			delete(f.sessions, k)
			n++
		}
	}
	return n, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// fakeIdP returns an httptest.Server impersonating an OIDC IdP. It exposes
// /token and /userinfo for the generic OAuth flow so the SSO handler can
// drive a full round trip without external network access.
type fakeIdP struct {
	server *httptest.Server
	// emailVerified controls the email_verified field in the userinfo
	// response so tests can assert the verified-email gate.
	emailVerified bool
	email         string
	name          string
}

func newFakeIdP(t *testing.T, email, name string, verified bool) *fakeIdP {
	idp := &fakeIdP{email: email, name: name, emailVerified: verified}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// Echo a fixed access token. PKCE verification is not enforced here
		// — the handler-side test asserts the verifier was sent.
		_ = r.ParseForm()
		if r.Form.Get("code") == "" || r.Form.Get("code_verifier") == "" {
			http.Error(w, "missing code or verifier", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fake-access-token","token_type":"bearer"}`)
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"sub":            "idp-sub-1",
			"email":          idp.email,
			"email_verified": idp.emailVerified,
			"name":           idp.name,
		})
		_, _ = w.Write(body)
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(func() { idp.server.Close() })
	return idp
}

// buildSSOTestServer stands up the auth HTTP handler with the SSO sub-
// service wired to in-memory fakes. Returns the running server, the SSO
// service for direct manipulation, and the tenant ID used by the fakes.
func buildSSOTestServer(t *testing.T) (*httptest.Server, *service.SSO, *fakeProviderRepo, *fakeSessionRepo, uuid.UUID) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, providers, sessions, key)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}

	tenantID := uuid.New()

	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "")
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Now that we know the test server's base URL, reset the SSO base URL
	// to that origin so redirect_uri matches in the token exchange.
	httpH.WithSSO(sso, srv.URL)
	return srv, sso, providers, sessions, tenantID
}

// keep imports referenced even if a subset of tests are skipped during refactor.
var _ = strings.NewReader

// ── Tests ───────────────────────────────────────────────────────────────────

func TestSSOListProviders_OnlyEnabled(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)

	// Seed two providers, one enabled and one disabled.
	enabled, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Sign in with Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    "https://idp.example.com",
		AutoProvision:     true,
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider enabled: %v", err)
	}
	_, err = sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Other (disabled)",
		Enabled:           false,
		OAuthClientID:     "client-id-2",
		OAuthClientSecret: "client-secret-2",
		OAuthIssuerURL:    "https://idp2.example.com",
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider disabled: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/auth/providers?tenant_id=" + tenantID.String())
	if err != nil {
		t.Fatalf("GET providers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, body=%s", resp.StatusCode, string(body))
	}
	var out struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Providers) != 1 {
		t.Fatalf("expected 1 enabled provider, got %d", len(out.Providers))
	}
	if out.Providers[0]["id"] != enabled.ID.String() {
		t.Errorf("wrong provider returned: %v", out.Providers[0])
	}
	// Defence in depth: secret-related fields must never appear.
	for _, k := range []string{"oauth_client_secret", "oauth_client_secret_enc", "client_secret"} {
		if _, ok := out.Providers[0][k]; ok {
			t.Errorf("response leaked %s", k)
		}
	}
}

func TestSSOStart_GeneratesStateAndRedirects(t *testing.T) {
	srv, sso, _, sessions, tenantID := buildSSOTestServer(t)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    "https://idp.example.com",
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/oauth/" + p.ID.String() + "/start?next=/repos")
	if err != nil {
		t.Fatalf("GET start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 302; body=%s", resp.StatusCode, string(body))
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "https://idp.example.com/authorize") {
		t.Errorf("wrong Location prefix: %s", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := u.Query()
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Errorf("missing state/challenge: %v", q)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("expected S256 challenge, got %s", q.Get("code_challenge_method"))
	}

	// One session row should now exist with the issued state.
	state := q.Get("state")
	if _, ok := sessions.sessions[state]; !ok {
		t.Errorf("session row not created for state=%s", state)
	}
}

func TestSSOStart_RejectsOpenRedirectNext(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    "https://idp.example.com",
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	bad := []string{
		"//evil.com",
		"https://evil.com",
		"http://evil.com/path",
		"javascript:alert(1)",
		"/foo\r\nLocation: http://evil",
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, n := range bad {
		u := srv.URL + "/auth/oauth/" + p.ID.String() + "/start?next=" + url.QueryEscape(n)
		resp, err := client.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", n, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("next=%q: want 400, got %d", n, resp.StatusCode)
		}
	}
}

func TestSSOCallback_HappyPath_AutoProvisions(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)
	idp := newFakeIdP(t, "alice@example.com", "Alice", true)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    idp.server.URL,
		AutoProvision:     true,
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	// Drive /start to mint a session.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/oauth/" + p.ID.String() + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")

	// Now drive /callback. The fake IdP will accept any code as long as a
	// verifier accompanies it.
	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake-code&state=%s",
		srv.URL, p.ID.String(), url.QueryEscape(state))
	resp, err = client.Get(cb)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback status: got %d, body=%s", resp.StatusCode, string(body))
	}
	dest := resp.Header.Get("Location")
	if !strings.HasPrefix(dest, "/dashboard") {
		t.Errorf("wrong dest: %s", dest)
	}
	du, _ := url.Parse(dest)
	tok := du.Query().Get("sso_token")
	if tok == "" {
		t.Error("missing sso_token in callback redirect")
	}
}

func TestSSOCallback_RejectsReplayedState(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)
	idp := newFakeIdP(t, "alice@example.com", "Alice", true)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    idp.server.URL,
		AutoProvision:     true,
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := client.Get(srv.URL + "/auth/oauth/" + p.ID.String() + "/start?next=/x")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")
	_ = resp.Body.Close()

	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s", srv.URL, p.ID.String(), state)
	// First callback succeeds.
	r1, _ := client.Get(cb)
	_ = r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("first callback: want 302, got %d", r1.StatusCode)
	}
	// Replay must fail (state is single-use).
	r2, _ := client.Get(cb)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: want 400, got %d", r2.StatusCode)
	}
}

func TestSSOCallback_RejectsUnverifiedEmail(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)
	idp := newFakeIdP(t, "evil@victim.com", "Evil", false)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:          tenantID,
		Type:              repository.AuthProviderOAuthGeneric,
		DisplayName:       "Acme",
		Enabled:           true,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthIssuerURL:    idp.server.URL,
		AutoProvision:     true,
		DefaultRole:       "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r1, _ := client.Get(srv.URL + "/auth/oauth/" + p.ID.String() + "/start?next=/x")
	loc, _ := url.Parse(r1.Header.Get("Location"))
	state := loc.Query().Get("state")
	_ = r1.Body.Close()

	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s", srv.URL, p.ID.String(), state)
	resp, _ := client.Get(cb)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 401 for unverified email, got %d; body=%s", resp.StatusCode, string(body))
	}
}

func TestSSOSAML_Returns501(t *testing.T) {
	srv, sso, _, _, tenantID := buildSSOTestServer(t)
	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:           tenantID,
		Type:               repository.AuthProviderSAML,
		DisplayName:        "Corp SAML",
		Enabled:            true,
		SAMLIdpMetadataXML: "<EntityDescriptor/>",
		DefaultRole:        "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider SAML: %v", err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/saml/" + p.ID.String() + "/start")
	if err != nil {
		t.Fatalf("GET saml start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("SAML start: want 501, got %d", resp.StatusCode)
	}
}

func TestSanitizeNextParam(t *testing.T) {
	good := []string{"", "/", "/foo", "/foo/bar", "/foo?baz=1"}
	for _, g := range good {
		if _, err := service.SanitizeNextParam(g); err != nil {
			t.Errorf("good %q rejected: %v", g, err)
		}
	}
	bad := []string{"//evil", "http://evil", "https://evil", "//", "javascript:1", "/\r\nx"}
	for _, b := range bad {
		if _, err := service.SanitizeNextParam(b); err == nil {
			t.Errorf("bad %q accepted", b)
		}
	}
}
