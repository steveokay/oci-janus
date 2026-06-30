// REDESIGN-001 RM-003 — HTTP handler tests for the SSO flow.
//
// Changes from FE-API-034:
//   - fakeProviderRepo replaced by fakeGlobalSSORepo (implements globalSSOConfigRepo).
//   - sso.CreateProvider removed; tests seed providers directly into the fake.
//   - provider_id is now a stable string (e.g. "generic") not a per-tenant UUID.
//   - TenantID removed from StartLoginInput and LoginSession.
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

// ── In-memory session fake ───────────────────────────────────────────────────

// fakeSessionRepo is the same as before — no changes needed.
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
	server        *httptest.Server
	emailVerified bool
	email         string
	name          string
	// subject overrides the default `idp-sub-1` value returned from the
	// `/userinfo` endpoint. SEC-043 tests mutate this between callbacks to
	// simulate an email-recycle / subject-recycle scenario where the same
	// email arrives back with a different IdP subject id.
	subject string
}

func newFakeIdP(t *testing.T, email, name string, verified bool) *fakeIdP {
	idp := &fakeIdP{email: email, name: name, emailVerified: verified, subject: "idp-sub-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
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
			"sub":            idp.subject,
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
// service, the global-provider fake, the session fake, and the dev tenant ID.
//
// REDESIGN-001 RM-003: uses fakeGlobalSSORepo instead of fakeProviderRepo.
func buildSSOTestServer(t *testing.T) (*httptest.Server, *service.SSO, *fakeGlobalSSORepo, *fakeSessionRepo, uuid.UUID) {
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
	return srv, sso, globalProviders, sessions, tenantID
}

// keep imports referenced even if a subset of tests are skipped during refactor.
var _ = strings.NewReader

// ── Tests ───────────────────────────────────────────────────────────────────

func TestSSOListProviders_OnlyEnabled(t *testing.T) {
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)

	// Seed two providers, one enabled and one disabled.
	const enabledID = "generic-enabled"
	const disabledID = "generic-disabled"
	globalProviders.Providers[enabledID] = &repository.GlobalSSOProvider{
		ProviderID:     enabledID,
		Kind:           "oauth_generic",
		DisplayName:    "Sign in with Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: "https://idp.example.com",
		AutoProvision:  true,
	}
	globalProviders.Providers[disabledID] = &repository.GlobalSSOProvider{
		ProviderID:     disabledID,
		Kind:           "oauth_generic",
		DisplayName:    "Other (disabled)",
		Enabled:        false,
		OAuthClientID:  "client-id-2",
		OAuthIssuerURL: "https://idp2.example.com",
	}

	resp, err := http.Get(srv.URL + "/api/v1/auth/providers")
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
	if out.Providers[0]["id"] != enabledID {
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
	srv, _, globalProviders, sessions, _ := buildSSOTestServer(t)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: "https://idp.example.com",
		AutoProvision:  true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/repos")
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
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: "https://idp.example.com",
		AutoProvision:  true,
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
		u := srv.URL + "/auth/oauth/" + providerID + "/start?next=" + url.QueryEscape(n)
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
	srv, _, globalProviders, _, tenantID := buildSSOTestServer(t)
	idp := newFakeIdP(t, "alice@example.com", "Alice", true)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: idp.server.URL,
		AutoProvision:  true,
	}
	// devDefaultTenant was set in buildSSOTestServer (= tenantID) — EnsureSSOUser
	// uses it when auto-provisioning a new user.
	_ = tenantID

	// Drive /start to mint a session.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")

	// Now drive /callback. The fake IdP will accept any code as long as a
	// verifier accompanies it.
	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake-code&state=%s",
		srv.URL, providerID, url.QueryEscape(state))
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
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)
	idp := newFakeIdP(t, "alice@example.com", "Alice", true)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: idp.server.URL,
		AutoProvision:  true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/x")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")
	_ = resp.Body.Close()

	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s", srv.URL, providerID, state)
	// First callback succeeds.
	r1, _ := client.Get(cb)
	_ = r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("first callback: want 302, got %d", r1.StatusCode)
	}
	// Replay must fail (state is single-use).
	r2, err := client.Get(cb)
	if err != nil {
		t.Fatalf("replay GET: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: want 400, got %d", r2.StatusCode)
	}
}

func TestSSOCallback_RejectsUnverifiedEmail(t *testing.T) {
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)
	idp := newFakeIdP(t, "evil@victim.com", "Evil", false)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: idp.server.URL,
		AutoProvision:  true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r1, _ := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/x")
	loc, _ := url.Parse(r1.Header.Get("Location"))
	state := loc.Query().Get("state")
	_ = r1.Body.Close()

	cb := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s", srv.URL, providerID, state)
	resp, err := client.Get(cb)
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 401 for unverified email, got %d; body=%s", resp.StatusCode, string(body))
	}
}

// TestSSOCallback_SEC043_SubjectMismatchReturns401WithGenericBody is the
// regression test for the security-agent finding on the SEC-040/041/042 PR:
// the service layer carefully built a generic, non-enumerating rejection
// message for ErrSSOSubjectMismatch (SEC-042), but the OAuth callback
// handler had no explicit case for that sentinel — it fell through to the
// default 500 INTERNAL branch, so the message never reached the wire.
//
// Test sequence:
//  1. First callback with sub=idp-sub-1 → 302 redirect, user auto-provisioned.
//  2. Mutate the IdP to return a different subject for the same email.
//  3. Second callback → must be 401 UNAUTHORIZED with the generic body
//     and the redirect path must NOT echo the email back.
func TestSSOCallback_SEC043_SubjectMismatchReturns401WithGenericBody(t *testing.T) {
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)
	const recycledEmail = "recycled@example.com"
	idp := newFakeIdP(t, recycledEmail, "First Holder", true)

	const providerID = "generic"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:     providerID,
		Kind:           "oauth_generic",
		DisplayName:    "Acme",
		Enabled:        true,
		OAuthClientID:  "client-id",
		OAuthIssuerURL: idp.server.URL,
		AutoProvision:  true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// First login: provisions the row with sso_subject = idp-sub-1.
	r1, err := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("start #1: %v", err)
	}
	_ = r1.Body.Close()
	loc1, _ := url.Parse(r1.Header.Get("Location"))
	state1 := loc1.Query().Get("state")
	cb1 := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s",
		srv.URL, providerID, url.QueryEscape(state1))
	resp1, err := client.Get(cb1)
	if err != nil {
		t.Fatalf("callback #1: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusFound {
		t.Fatalf("callback #1: want 302, got %d", resp1.StatusCode)
	}

	// Second login: same email, new subject. This is the recycled-email path.
	idp.subject = "idp-sub-NEW-HIRE"
	r2, err := client.Get(srv.URL + "/auth/oauth/" + providerID + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("start #2: %v", err)
	}
	_ = r2.Body.Close()
	loc2, _ := url.Parse(r2.Header.Get("Location"))
	state2 := loc2.Query().Get("state")
	cb2 := fmt.Sprintf("%s/auth/oauth/%s/callback?code=fake&state=%s",
		srv.URL, providerID, url.QueryEscape(state2))
	resp2, err := client.Get(cb2)
	if err != nil {
		t.Fatalf("callback #2: %v", err)
	}
	defer resp2.Body.Close()

	// SEC-043 — must be 401 with the generic body, NOT a default 500.
	if resp2.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("subject mismatch must return 401, got %d; body=%s", resp2.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "contact your admin") {
		t.Errorf("SEC-043: response body must use the generic SEC-042 phrasing; got %s", string(body))
	}
	// SEC-042 — the email must never reach the wire.
	if strings.Contains(string(body), recycledEmail) {
		t.Errorf("SEC-042: response body must not leak the email; got %s", string(body))
	}
	if strings.Contains(resp2.Header.Get("Location"), recycledEmail) {
		t.Errorf("SEC-042: redirect target must not leak the email; Location=%s", resp2.Header.Get("Location"))
	}
}

func TestSSOSAML_Returns501(t *testing.T) {
	srv, _, globalProviders, _, _ := buildSSOTestServer(t)

	// Seed a SAML provider.
	const providerID = "saml"
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:      providerID,
		Kind:            "saml",
		DisplayName:     "Corp SAML",
		Enabled:         true,
		SAMLMetadataXML: []byte("<EntityDescriptor/>"),
		AutoProvision:   true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/saml/" + providerID + "/start")
	if err != nil {
		t.Fatalf("GET saml start: %v", err)
	}
	defer resp.Body.Close()
	// No SAML SP cert configured → 501 NOT_CONFIGURED.
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
