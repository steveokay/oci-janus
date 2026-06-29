// REDESIGN-001 RM-003 — handler tests for the SAML SP-initiated flow.
//
// Changes from FE-API-034:
//   - fakeProviderRepo replaced by fakeGlobalSSORepo (implements globalSSOConfigRepo).
//   - provider_id is now a stable string (e.g. "saml") not a UUID.
//   - sso.CreateProvider removed; tests seed providers directly in the fake.
//   - sso.CreateSAMLLoginSession signature: (ctx, providerID string, authnReqID, nextURL string).
//   - repository.AuthProvider → repository.GlobalSSOProvider.
package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	crewjamsaml "github.com/crewjam/saml"
	crewjamsamlsp "github.com/crewjam/saml/samlsp"
	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authsaml "github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── fakeGlobalSSORepo ───────────────────────────────────────────────────────

// fakeGlobalSSORepo is an in-memory implementation of globalSSOConfigRepo.
// Tests seed Providers directly by string key.
type fakeGlobalSSORepo struct {
	mu        sync.Mutex
	Providers map[string]*repository.GlobalSSOProvider
}

func newFakeGlobalSSORepo() *fakeGlobalSSORepo {
	return &fakeGlobalSSORepo{Providers: make(map[string]*repository.GlobalSSOProvider)}
}

func (f *fakeGlobalSSORepo) Get(_ context.Context, providerID string) (*repository.GlobalSSOProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.Providers[providerID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	out := *p
	return &out, nil
}

func (f *fakeGlobalSSORepo) List(_ context.Context, enabledOnly bool) ([]*repository.GlobalSSOProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*repository.GlobalSSOProvider, 0, len(f.Providers))
	for _, p := range f.Providers {
		if enabledOnly && !p.Enabled {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

// ── keypair + IdP helpers ───────────────────────────────────────────────────

// genTestKeypair returns a fresh RSA keypair + a self-signed X.509 cert with
// a one-year validity window. Used by both the test IdP and the test SP so
// the suite has no cert/key files on disk.
func genTestKeypair(t *testing.T, commonName string) (*rsa.PrivateKey, *x509.Certificate, []byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return key, cert, certPEM, keyPEM
}

// testIDP wraps a crewjam/saml IdentityProvider with helpers to drive an
// SP-initiated flow end-to-end inside one process.
type testIDP struct {
	idp        *crewjamsaml.IdentityProvider
	server     *httptest.Server
	spMetadata *crewjamsaml.EntityDescriptor
	// userEmail / userName control the assertion attributes returned for
	// each request.
	userEmail string
	userName  string
}

// testSPMetadataProvider is a tiny ServiceProviderProvider that returns the
// single SP metadata document we register at IdP construction time. The IdP
// only ever serves our one SP, so we don't need to dispatch on the SP ID.
type testSPMetadataProvider struct {
	md *crewjamsaml.EntityDescriptor
}

func (t *testSPMetadataProvider) GetServiceProvider(_ *http.Request, _ string) (*crewjamsaml.EntityDescriptor, error) {
	return t.md, nil
}

// testSessionProvider always returns a fixed Session — that's enough to drive
// the IdP into MakeResponse without any real authentication UI.
type testSessionProvider struct {
	email string
	name  string
}

func (t *testSessionProvider) GetSession(_ http.ResponseWriter, _ *http.Request, _ *crewjamsaml.IdpAuthnRequest) *crewjamsaml.Session {
	return &crewjamsaml.Session{
		ID:             "test-session-1",
		CreateTime:     time.Now().Add(-time.Minute),
		ExpireTime:     time.Now().Add(time.Hour),
		Index:          "test-idx",
		NameID:         t.email,
		NameIDFormat:   "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		UserName:       t.email,
		UserEmail:      t.email,
		UserCommonName: t.name,
	}
}

// newTestIDP returns an in-process IdP, with a sessionless SP descriptor
// registered for it. spMetadataXML is the SP metadata the SAML handler would
// publish — we feed it back to the IdP so it knows where to send responses.
func newTestIDP(t *testing.T, spMetadataXML []byte, email, name string) *testIDP {
	t.Helper()
	idpKey, idpCert, _, _ := genTestKeypair(t, "test-idp")

	spMD, err := crewjamsamlsp.ParseMetadata(spMetadataXML)
	if err != nil {
		t.Fatalf("parse SP metadata: %v", err)
	}

	// Allocate a listener up front so we know the origin BEFORE building the
	// IdP — crewjam/saml's IdentityProvider.Handler() reads SSOURL/MetadataURL
	// during construction and uses them as ServeMux patterns.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	origin := "http://" + listener.Addr().String()
	ssoURL, _ := url.Parse(origin + "/sso")
	metaURL, _ := url.Parse(origin + "/metadata")

	idp := &crewjamsaml.IdentityProvider{
		Key:                     idpKey,
		Certificate:             idpCert,
		SSOURL:                  *ssoURL,
		MetadataURL:             *metaURL,
		ServiceProviderProvider: &testSPMetadataProvider{md: spMD},
		SessionProvider:         &testSessionProvider{email: email, name: name},
		SignatureMethod:         "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256",
	}

	srv := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: idp.Handler()},
	}
	srv.Start()
	srv.URL = origin
	t.Cleanup(srv.Close)

	return &testIDP{
		idp:        idp,
		server:     srv,
		spMetadata: spMD,
		userEmail:  email,
		userName:   name,
	}
}

// produceSignedResponse drives the IdP to produce a signed Response form for
// the given AuthnRequest URL (the one the SP redirected the browser to).
// Returns the SAMLResponse + RelayState fields that the browser would POST
// to the SP's ACS endpoint.
func (t *testIDP) produceSignedResponse(tb testing.TB, authnURL string) (samlResponse, relayState string) {
	tb.Helper()

	req, err := http.NewRequest(http.MethodGet, authnURL, nil)
	if err != nil {
		tb.Fatalf("build SSO request: %v", err)
	}
	w := httptest.NewRecorder()
	t.idp.ServeSSO(w, req)
	if w.Code != http.StatusOK {
		tb.Fatalf("IdP ServeSSO status=%d, body=%s", w.Code, w.Body.String())
	}

	body := w.Body.Bytes()
	samlResponse = extractFormInput(body, "SAMLResponse")
	relayState = extractFormInput(body, "RelayState")
	if samlResponse == "" {
		tb.Fatalf("IdP response body missing SAMLResponse; body=%s", w.Body.String())
	}
	return samlResponse, relayState
}

// extractFormInput pulls the value of a hidden input field out of the IdP's
// rendered HTML form.
func extractFormInput(body []byte, name string) string {
	needle := []byte(`name="` + name + `" value="`)
	idx := bytes.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return ""
	}
	return string(body[start : start+end])
}

// ── SAML-enabled test server builder ────────────────────────────────────────

// buildSSOTestServerWithSAML mirrors buildSSOTestServer but also wires a
// freshly-generated SP keypair via WithSAMLConfig.
//
// REDESIGN-001 RM-003: uses fakeGlobalSSORepo instead of fakeProviderRepo.
func buildSSOTestServerWithSAML(t *testing.T) (srv *httptest.Server, sso *service.SSO, sessions *fakeSessionRepo, tenantID uuid.UUID) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	globalProviders := newFakeGlobalSSORepo()
	sessions = newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, globalProviders, sessions, key)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}

	tenantID = uuid.New()

	// Generate SP keypair in-process — no testdata files needed.
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}

	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Reset the SSO base URL to the test server origin so the ACS URL the SP
	// publishes in its metadata matches the test server's ACS endpoint.
	httpH.WithSSO(sso, srv.URL)
	return srv, sso, sessions, tenantID
}

// buildSPMetadataForProvider returns the SP metadata XML the test IdP needs
// in order to issue responses targeted at this exact provider's ACS URL.
// provider_id is now a string (e.g. "saml").
func buildSPMetadataForProvider(t *testing.T, srvURL string, providerID string, key *rsa.PrivateKey, cert *x509.Certificate, entityID string) []byte {
	t.Helper()
	acsURL, _ := url.Parse(srvURL + "/auth/saml/" + providerID + "/acs")
	sp := &crewjamsaml.ServiceProvider{
		EntityID:    entityID,
		Key:         key,
		Certificate: cert,
		AcsURL:      *acsURL,
	}
	md := sp.Metadata()
	out, err := xml.Marshal(md)
	if err != nil {
		t.Fatalf("marshal SP metadata: %v", err)
	}
	return out
}

// ── Tests: LoadSPConfig + ExtractAttribute ──────────────────────────────────

func TestSAMLLoadSPConfig_ValidPEM(t *testing.T) {
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	cfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}
	if cfg.Certificate == nil || cfg.Key == nil {
		t.Fatal("LoadSPConfig returned nil cert or key")
	}
}

func TestSAMLLoadSPConfig_RejectsEmpty(t *testing.T) {
	if _, err := authsaml.LoadSPConfig(nil, nil); err == nil {
		t.Error("expected error for empty cert+key")
	}
}

func TestSAMLLoadSPConfig_RejectsMalformed(t *testing.T) {
	if _, err := authsaml.LoadSPConfig([]byte("not a pem"), []byte("not a pem")); err == nil {
		t.Error("expected error for malformed PEM")
	}
}

func TestSAMLExtractAttribute(t *testing.T) {
	a := &crewjamsaml.Assertion{
		AttributeStatements: []crewjamsaml.AttributeStatement{
			{
				Attributes: []crewjamsaml.Attribute{
					{
						Name:         "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
						FriendlyName: "email",
						Values:       []crewjamsaml.AttributeValue{{Value: "alice@example.com"}},
					},
					{
						Name:   "displayName",
						Values: []crewjamsaml.AttributeValue{{Value: "Alice"}},
					},
				},
			},
		},
	}
	if got := authsaml.ExtractAttribute(a, "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"); got != "alice@example.com" {
		t.Errorf("URN lookup: got %q", got)
	}
	if got := authsaml.ExtractAttribute(a, "email"); got != "alice@example.com" {
		t.Errorf("FriendlyName lookup: got %q", got)
	}
	if got := authsaml.ExtractAttribute(a, "displayName"); got != "Alice" {
		t.Errorf("short-name lookup: got %q", got)
	}
	if got := authsaml.ExtractAttribute(a, "missing"); got != "" {
		t.Errorf("missing attribute should return empty, got %q", got)
	}
	if got := authsaml.ExtractAttribute(nil, "email"); got != "" {
		t.Errorf("nil assertion should return empty, got %q", got)
	}
}

// ── Tests: handler flow ─────────────────────────────────────────────────────

// TestSAMLStart_BuildsAuthnRequestAndRedirects asserts startSAML mints a
// session and 302s to the IdP's SSO URL with a SAMLRequest query param.
func TestSAMLStart_BuildsAuthnRequestAndRedirects(t *testing.T) {
	srv, _, sessions, tenantID := buildSSOTestServerWithSAML(t)
	// Extract the sso + globalProviders from a fresh build so we can seed.
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	globalProviders := newFakeGlobalSSORepo()
	sessRepo := newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, globalProviders, sessRepo, key)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}

	// Use a fixed providerID string.
	const providerID = "saml"

	// Spin up a throwaway test IdP to harvest valid IdP metadata XML.
	dummySPMD := dummySPMetadataXML(t)
	idp := newTestIDP(t, dummySPMD, "noop@example.com", "Noop")
	idpMD := idpMetadataXML(t, idp.idp)

	// Seed provider directly.
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:      providerID,
		Kind:            "saml",
		DisplayName:     "Corp SAML",
		Enabled:         true,
		SAMLMetadataXML: idpMD,
		AutoProvision:   true,
	}

	mux2 := http.NewServeMux()
	httpH2 := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH2.Register(mux2)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	httpH2.WithSSO(sso, srv2.URL)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv2.URL + "/auth/saml/" + providerID + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("GET start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 302; body=%s", resp.StatusCode, string(body))
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, idp.server.URL+"/sso") {
		t.Errorf("wrong redirect target: %s", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if u.Query().Get("SAMLRequest") == "" {
		t.Error("missing SAMLRequest query param")
	}
	relayState := u.Query().Get("RelayState")
	if relayState == "" {
		t.Error("missing RelayState query param")
	}
	// One session row should now exist keyed by the RelayState (in sessRepo).
	if _, ok := sessRepo.sessions[relayState]; !ok {
		t.Errorf("session row not created for relay_state=%s", relayState)
	}
	// Suppress unused variable warning from earlier buildSSOTestServerWithSAML call.
	_ = srv
	_ = sessions
}

// TestSAMLCallback_HappyPath drives a full SP-initiated flow end-to-end via
// the in-process IdP, then verifies the SP issued a JWT and 302'd back to
// the original `next` URL.
func TestSAMLCallback_HappyPath(t *testing.T) {
	spKey, spCert, certPEM, keyPEM := genTestKeypair(t, "test-sp")

	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, globalProviders, sessions, credKey)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// WithSAMLTrustEmail(true) — this test predates Phase 5.6 and exercises
	// the post-trust happy path. The trust-flag behaviour itself is covered
	// by TestSAMLCallback_TrustEmailFlag.
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg).WithSAMLTrustEmail(true)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	// provider_id is a stable string in the new model.
	const providerID = "saml"
	entityID := srv.URL + "/auth/saml/metadata"
	spMetadataXML := buildSPMetadataForProvider(t, srv.URL, providerID, spKey, spCert, entityID)
	idp := newTestIDP(t, spMetadataXML, "alice@example.com", "Alice")
	idpMD := idpMetadataXML(t, idp.idp)

	// Seed provider directly with the string ID.
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:      providerID,
		Kind:            "saml",
		DisplayName:     "Corp SAML",
		Enabled:         true,
		SAMLMetadataXML: idpMD,
		AutoProvision:   true,
	}

	// /start — redirect to IdP carrying AuthnRequest + RelayState.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/saml/" + providerID + "/start?next=/dashboard")
	if err != nil {
		t.Fatalf("GET start: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("start status: got %d, body=%s", resp.StatusCode, string(body))
	}
	_ = resp.Body.Close()
	authnURL := resp.Header.Get("Location")

	// Drive the IdP to produce a signed response targeting our SP.
	samlResponse, relayState := idp.produceSignedResponse(t, authnURL)
	if samlResponse == "" || relayState == "" {
		t.Fatal("IdP did not produce SAMLResponse + RelayState")
	}

	// POST the response to the SP's ACS endpoint.
	form := url.Values{}
	form.Set("SAMLResponse", samlResponse)
	form.Set("RelayState", relayState)
	cbResp, err := client.PostForm(srv.URL+"/auth/saml/"+providerID+"/acs", form)
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(cbResp.Body)
		t.Fatalf("acs status: got %d, want 302; body=%s", cbResp.StatusCode, string(body))
	}
	dest := cbResp.Header.Get("Location")
	if !strings.HasPrefix(dest, "/dashboard") {
		t.Errorf("wrong dest: %s", dest)
	}
	du, _ := url.Parse(dest)
	if du.Query().Get("sso_token") == "" {
		t.Error("missing sso_token in callback redirect")
	}
}

// TestSAMLCallback_RejectsReplayedRelayState confirms a second submission of
// the same RelayState is rejected even if the SAMLResponse signature is still
// valid.
func TestSAMLCallback_RejectsReplayedRelayState(t *testing.T) {
	spKey, spCert, certPEM, keyPEM := genTestKeypair(t, "test-sp")

	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, globalProviders, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// WithSAMLTrustEmail(true) — this test predates Phase 5.6; trust-flag
	// coverage lives in TestSAMLCallback_TrustEmailFlag.
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg).WithSAMLTrustEmail(true)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	const providerID = "saml"
	entityID := srv.URL + "/auth/saml/metadata"
	spMetadataXML := buildSPMetadataForProvider(t, srv.URL, providerID, spKey, spCert, entityID)
	idp := newTestIDP(t, spMetadataXML, "alice@example.com", "Alice")
	idpMD := idpMetadataXML(t, idp.idp)
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:      providerID,
		Kind:            "saml",
		DisplayName:     "Corp SAML",
		Enabled:         true,
		SAMLMetadataXML: idpMD,
		AutoProvision:   true,
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := client.Get(srv.URL + "/auth/saml/" + providerID + "/start?next=/x")
	authnURL := resp.Header.Get("Location")
	_ = resp.Body.Close()

	samlResponse, relayState := idp.produceSignedResponse(t, authnURL)
	form := url.Values{}
	form.Set("SAMLResponse", samlResponse)
	form.Set("RelayState", relayState)
	r1, _ := client.PostForm(srv.URL+"/auth/saml/"+providerID+"/acs", form)
	_ = r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("first ACS: want 302, got %d", r1.StatusCode)
	}
	// Replay must fail — RelayState already consumed.
	r2, err := client.PostForm(srv.URL+"/auth/saml/"+providerID+"/acs", form)
	if err != nil {
		t.Fatalf("replay POST acs: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: want 400, got %d", r2.StatusCode)
	}
}

// TestSAMLCallback_MissingFormFields asserts a POST without SAMLResponse or
// RelayState is rejected with 400.
func TestSAMLCallback_MissingFormFields(t *testing.T) {
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, globalProviders, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	// Provider doesn't need to exist for this test — the missing-form-fields
	// branch fires before provider lookup.
	resp, err := http.PostForm(srv.URL+"/auth/saml/anyprovider/acs", url.Values{})
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

// TestSAMLCallback_MalformedSAMLResponse asserts a POST with a bogus
// SAMLResponse value is rejected with 400 INVALIDSAML.
func TestSAMLCallback_MalformedSAMLResponse(t *testing.T) {
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, globalProviders, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	const providerID = "saml"
	// Seed a valid IdP metadata XML so SP construction succeeds before
	// ParseResponse fails on the bogus SAMLResponse.
	dummySPMD := dummySPMetadataXML(t)
	idp := newTestIDP(t, dummySPMD, "noop@example.com", "Noop")
	idpMD := idpMetadataXML(t, idp.idp)
	globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
		ProviderID:      providerID,
		Kind:            "saml",
		DisplayName:     "Corp",
		Enabled:         true,
		SAMLMetadataXML: idpMD,
		AutoProvision:   true,
	}

	// Mint a real relay state so ConsumeByState succeeds and we reach ParseResponse.
	// RM-003/RM-004: CreateSAMLLoginSession now takes (ctx, providerID string, authnReqID, nextURL string).
	relayState, err := sso.CreateSAMLLoginSession(context.Background(), providerID, "dummy-authn-req-id", "/x")
	if err != nil {
		t.Fatalf("CreateSAMLLoginSession: %v", err)
	}

	form := url.Values{}
	form.Set("SAMLResponse", "not-a-real-saml-response")
	form.Set("RelayState", relayState)
	resp, err := http.PostForm(srv.URL+"/auth/saml/"+providerID+"/acs", form)
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("want 400 INVALIDSAML, got %d; body=%s", resp.StatusCode, string(body))
	}
}

// TestSAMLCallback_NotConfiguredReturns501 confirms a deployment without the
// SAML SP keypair returns 501 on ACS.
func TestSAMLCallback_NotConfiguredReturns501(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	globalProviders := newFakeGlobalSSORepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, globalProviders, sessions, credKey)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// NO WithSAMLConfig.
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "")
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	form := url.Values{}
	form.Set("SAMLResponse", "x")
	form.Set("RelayState", "y")
	resp, err := http.PostForm(srv.URL+"/auth/saml/anyprovider/acs", form)
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("want 501 when SAML not configured, got %d", resp.StatusCode)
	}
}

// ── Phase 5.6 — SSO_SAML_TRUST_EMAIL flag ───────────────────────────────────

// TestSAMLCallback_TrustEmailFlag covers REDESIGN-001 Phase 5.6 — the
// SSO_SAML_TRUST_EMAIL config flag. The cases are:
//
//   - trust=true  → new user auto-provisioned, 302 with sso_token (existing
//     happy-path semantics preserved).
//   - trust=false → SAML callback refused with 403 EMAILNOTVERIFIED (because
//     EnsureSSOUser's existing gate kicks in for SSOIdentity.EmailVerified=false).
//   - default (handler never sees WithSAMLTrustEmail) → same as trust=false;
//     verifies the fail-safe zero value matches the documented default.
func TestSAMLCallback_TrustEmailFlag(t *testing.T) {
	cases := []struct {
		name       string
		applyFlag  func(h *HTTPHandler) *HTTPHandler
		wantStatus int
		// wantSSOToken is true when the redirect should carry an sso_token
		// query param; only the trust=true branch reaches IssueSSOToken.
		wantSSOToken bool
	}{
		{
			name:         "trust_true_provisions_and_issues_jwt",
			applyFlag:    func(h *HTTPHandler) *HTTPHandler { return h.WithSAMLTrustEmail(true) },
			wantStatus:   http.StatusFound,
			wantSSOToken: true,
		},
		{
			name:         "trust_false_refuses_with_403",
			applyFlag:    func(h *HTTPHandler) *HTTPHandler { return h.WithSAMLTrustEmail(false) },
			wantStatus:   http.StatusForbidden,
			wantSSOToken: false,
		},
		{
			name:         "default_unset_matches_trust_false_failsafe",
			applyFlag:    func(h *HTTPHandler) *HTTPHandler { return h }, // never call WithSAMLTrustEmail
			wantStatus:   http.StatusForbidden,
			wantSSOToken: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spKey, spCert, certPEM, keyPEM := genTestKeypair(t, "test-sp")
			ts, cleanup := buildTestService(t)
			t.Cleanup(cleanup)

			globalProviders := newFakeGlobalSSORepo()
			sessions := newFakeSessionRepo()
			credKey := make([]byte, 32)
			for i := range credKey {
				credKey[i] = byte(i)
			}
			sso, err := service.NewSSO(ts.svc, globalProviders, sessions, credKey)
			if err != nil {
				t.Fatalf("NewSSO: %v", err)
			}
			spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
			if err != nil {
				t.Fatalf("LoadSPConfig: %v", err)
			}

			tenantID := uuid.New()
			mux := http.NewServeMux()
			httpH := NewHTTPHandler(ts.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
			httpH = tc.applyFlag(httpH)
			httpH.Register(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			httpH.WithSSO(sso, srv.URL)

			const providerID = "saml"
			entityID := srv.URL + "/auth/saml/metadata"
			spMetadataXML := buildSPMetadataForProvider(t, srv.URL, providerID, spKey, spCert, entityID)
			// Use a per-case email so the trust=true subtest can't collide
			// with a row left by a sibling subtest sharing the in-memory
			// store. (buildTestService rebuilds the DB per call, but be
			// defensive.)
			idp := newTestIDP(t, spMetadataXML, "bob+"+tc.name+"@example.com", "Bob")
			idpMD := idpMetadataXML(t, idp.idp)

			globalProviders.Providers[providerID] = &repository.GlobalSSOProvider{
				ProviderID:      providerID,
				Kind:            "saml",
				DisplayName:     "Corp SAML",
				Enabled:         true,
				SAMLMetadataXML: idpMD,
				AutoProvision:   true,
			}

			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Get(srv.URL + "/auth/saml/" + providerID + "/start?next=/dashboard")
			if err != nil {
				t.Fatalf("GET start: %v", err)
			}
			if resp.StatusCode != http.StatusFound {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				t.Fatalf("start status: got %d, body=%s", resp.StatusCode, string(body))
			}
			_ = resp.Body.Close()
			authnURL := resp.Header.Get("Location")

			samlResponse, relayState := idp.produceSignedResponse(t, authnURL)
			if samlResponse == "" || relayState == "" {
				t.Fatal("IdP did not produce SAMLResponse + RelayState")
			}

			form := url.Values{}
			form.Set("SAMLResponse", samlResponse)
			form.Set("RelayState", relayState)
			cbResp, err := client.PostForm(srv.URL+"/auth/saml/"+providerID+"/acs", form)
			if err != nil {
				t.Fatalf("POST acs: %v", err)
			}
			defer cbResp.Body.Close()
			if cbResp.StatusCode != tc.wantStatus {
				body, _ := io.ReadAll(cbResp.Body)
				t.Fatalf("acs status: got %d, want %d; body=%s", cbResp.StatusCode, tc.wantStatus, string(body))
			}

			if tc.wantSSOToken {
				dest := cbResp.Header.Get("Location")
				if !strings.HasPrefix(dest, "/dashboard") {
					t.Errorf("trust=true: wrong dest: %s", dest)
				}
				du, _ := url.Parse(dest)
				if du.Query().Get("sso_token") == "" {
					t.Error("trust=true: missing sso_token in callback redirect")
				}
			} else {
				// Refused branch — make sure no sso_token leaks via redirect
				// (writeError returns a JSON body, not a redirect).
				if loc := cbResp.Header.Get("Location"); loc != "" {
					t.Errorf("refused branch should not redirect; got Location=%s", loc)
				}
			}
		})
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// idpMetadataXML returns the IdP's metadata document as XML bytes.
func idpMetadataXML(t *testing.T, idp *crewjamsaml.IdentityProvider) []byte {
	t.Helper()
	md := idp.Metadata()
	out, err := xml.Marshal(md)
	if err != nil {
		t.Fatalf("marshal IdP metadata: %v", err)
	}
	return out
}

// dummySPMetadataXML returns a minimal SP metadata document used when a test
// needs ANY SP metadata to satisfy newTestIDP but never actually drives the
// IdP through ServeSSO.
func dummySPMetadataXML(t *testing.T) []byte {
	t.Helper()
	_, _, certPEM, keyPEM := genTestKeypair(t, "dummy-sp")
	cfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}
	acsURL, _ := url.Parse("http://example.invalid/acs")
	sp := &crewjamsaml.ServiceProvider{
		EntityID:    "http://example.invalid/metadata",
		Key:         cfg.Key,
		Certificate: cfg.Certificate,
		AcsURL:      *acsURL,
	}
	out, err := xml.Marshal(sp.Metadata())
	if err != nil {
		t.Fatalf("marshal dummy SP metadata: %v", err)
	}
	return out
}
