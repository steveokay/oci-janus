// FE-API-034 — handler tests for the SAML SP-initiated flow.
//
// We don't ship test fixtures on disk — every keypair is generated in-process
// via crypto/x509.CreateCertificate so the tests are deterministic and have
// no expiry. The IdP is the real crewjam/saml.IdentityProvider driven through
// its public API, which gives us signed responses that the SP can validate
// end-to-end without any hand-rolled XML.
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
	"testing"
	"time"

	crewjamsaml "github.com/crewjam/saml"
	crewjamsamlsp "github.com/crewjam/saml/samlsp"
	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authsaml "github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

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
	// during construction and uses them as ServeMux patterns, which only works
	// when they are full URLs filled in ahead of time.
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
	// httptest.Server.Start sets URL from Listener when URL is empty, but
	// belt-and-braces: assign so other helpers can read it.
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

	// Hit the IdP's SSO endpoint with the SAML AuthnRequest. The IdP's
	// Handler parses the AuthnRequest, invokes the SessionProvider, and
	// renders an HTML form whose hidden inputs are SAMLResponse + RelayState.
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
// rendered HTML form. The IdP's template is stable across crewjam/saml v0.4.x
// so this brittle-looking parse is fine for tests.
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
// freshly-generated SP keypair via WithSAMLConfig. Tests that need to drive
// a real IdP construct their own server inline so they control the SP
// keypair (the IdP needs to see the SP metadata pinned to a specific ACS
// URL, which requires the provider ID up front).
func buildSSOTestServerWithSAML(t *testing.T) (srv *httptest.Server, sso *service.SSO, sessions *fakeSessionRepo, tenantID uuid.UUID) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	providers := newFakeProviderRepo()
	sessions = newFakeSessionRepo()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, providers, sessions, key)
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
// Called after we've created the provider so the metadata's AcsURL reflects
// the real path /auth/saml/{provider_id}/acs.
func buildSPMetadataForProvider(t *testing.T, srvURL string, providerID uuid.UUID, key *rsa.PrivateKey, cert *x509.Certificate, entityID string) []byte {
	t.Helper()
	acsURL, _ := url.Parse(srvURL + "/auth/saml/" + providerID.String() + "/acs")
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
	srv, sso, sessions, tenantID := buildSSOTestServerWithSAML(t)

	// We need the SP keypair to build SP metadata for the IdP. Rather than
	// thread it back through the helper return, re-extract it from the
	// handler — easier: just generate a SECOND keypair and use it both as
	// the test IdP's reference AND as a fake provider metadata. Simpler
	// still: generate the test IdP, get its metadata XML, feed THAT into
	// CreateProvider as the IdP metadata. Then the SP cert in the handler
	// doesn't need to match anything the IdP sees.
	//
	// (For start, we never validate signatures — just check the redirect.)

	// Spin up a throwaway test IdP just to harvest a valid IdP metadata XML.
	// SP metadata for the throwaway IdP is just our SP cert, but the IdP
	// won't actually be hit in this test — startSAML stops at the redirect.
	dummySPMD := dummySPMetadataXML(t)
	idp := newTestIDP(t, dummySPMD, "noop@example.com", "Noop")
	idpMD := idpMetadataXML(t, idp.idp)

	p, err := sso.CreateProvider(t.Context(), service.CreateProviderInput{
		TenantID:           tenantID,
		Type:               repository.AuthProviderSAML,
		DisplayName:        "Corp SAML",
		Enabled:            true,
		SAMLIdpMetadataXML: string(idpMD),
		AutoProvision:      true,
		DefaultRole:        "reader",
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/saml/" + p.ID.String() + "/start?next=/dashboard")
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
	if u.Query().Get("RelayState") == "" {
		t.Error("missing RelayState query param")
	}

	// One session row should now exist keyed by the RelayState.
	relayState := u.Query().Get("RelayState")
	if _, ok := sessions.sessions[relayState]; !ok {
		t.Errorf("session row not created for relay_state=%s", relayState)
	}
}

// TestSAMLCallback_HappyPath drives a full SP-initiated flow end-to-end via
// the in-process IdP, then verifies the SP issued a JWT and 302'd back to
// the original `next` URL.
func TestSAMLCallback_HappyPath(t *testing.T) {
	spKey, spCert, certPEM, keyPEM := genTestKeypair(t, "test-sp")

	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, err := service.NewSSO(tc.svc, providers, sessions, credKey)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadSPConfig: %v", err)
	}

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	// Create the provider with a placeholder metadata so we know its ID,
	// then rebuild proper IdP metadata once we have the SP metadata that
	// references the real ACS URL.
	providerID := uuid.New()
	// Stand up the IdP using SP metadata pinned to this providerID.
	entityID := srv.URL + "/auth/saml/metadata"
	spMetadataXML := buildSPMetadataForProvider(t, srv.URL, providerID, spKey, spCert, entityID)
	idp := newTestIDP(t, spMetadataXML, "alice@example.com", "Alice")
	idpMD := idpMetadataXML(t, idp.idp)

	// Inject the provider row directly so its ID matches what we baked into
	// the SP metadata above (avoids a chicken-and-egg loop between provider
	// creation and ACS URL).
	providers.providers[providerID] = &repository.AuthProvider{
		ID:                 providerID,
		TenantID:           tenantID,
		Type:               repository.AuthProviderSAML,
		DisplayName:        "Corp SAML",
		Enabled:            true,
		SAMLIdpMetadataXML: string(idpMD),
		SAMLEntityID:       entityID,
		AutoProvision:      true,
		DefaultRole:        "reader",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	// /start — redirect to IdP carrying AuthnRequest + RelayState.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/saml/" + providerID.String() + "/start?next=/dashboard")
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
	cbResp, err := client.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", form)
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
// valid. The single-use rule is enforced by ConsumeByState on the login
// session — the second consume returns ErrSessionNotFound → 400.
func TestSAMLCallback_RejectsReplayedRelayState(t *testing.T) {
	spKey, spCert, certPEM, keyPEM := genTestKeypair(t, "test-sp")

	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, providers, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	providerID := uuid.New()
	entityID := srv.URL + "/auth/saml/metadata"
	spMetadataXML := buildSPMetadataForProvider(t, srv.URL, providerID, spKey, spCert, entityID)
	idp := newTestIDP(t, spMetadataXML, "alice@example.com", "Alice")
	idpMD := idpMetadataXML(t, idp.idp)
	providers.providers[providerID] = &repository.AuthProvider{
		ID:                 providerID,
		TenantID:           tenantID,
		Type:               repository.AuthProviderSAML,
		DisplayName:        "Corp SAML",
		Enabled:            true,
		SAMLIdpMetadataXML: string(idpMD),
		SAMLEntityID:       entityID,
		AutoProvision:      true,
		DefaultRole:        "reader",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := client.Get(srv.URL + "/auth/saml/" + providerID.String() + "/start?next=/x")
	authnURL := resp.Header.Get("Location")
	_ = resp.Body.Close()

	samlResponse, relayState := idp.produceSignedResponse(t, authnURL)
	form := url.Values{}
	form.Set("SAMLResponse", samlResponse)
	form.Set("RelayState", relayState)
	r1, _ := client.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", form)
	_ = r1.Body.Close()
	if r1.StatusCode != http.StatusFound {
		t.Fatalf("first ACS: want 302, got %d", r1.StatusCode)
	}
	// Replay must fail — RelayState already consumed.
	r2, err := client.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", form)
	if err != nil {
		t.Fatalf("replay POST acs: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: want 400, got %d", r2.StatusCode)
	}
}

// TestSAMLCallback_MissingFormFields asserts a POST without SAMLResponse or
// RelayState is rejected with 400 — defence in depth before the SP touches
// the XML.
func TestSAMLCallback_MissingFormFields(t *testing.T) {
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, providers, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	// Provider doesn't need to exist for this test — the missing-form-fields
	// branch fires before provider lookup. But we still need SOME provider
	// id in the URL.
	providerID := uuid.New()
	resp, err := http.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", url.Values{})
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

// TestSAMLCallback_MalformedSAMLResponse asserts a POST with a bogus
// SAMLResponse value is rejected with 400 INVALIDSAML. We supply a valid
// RelayState (consume succeeds) so the failure has to come from ParseResponse.
func TestSAMLCallback_MalformedSAMLResponse(t *testing.T) {
	_, _, certPEM, keyPEM := genTestKeypair(t, "test-sp")
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, providers, sessions, credKey)
	spCfg, _ := authsaml.LoadSPConfig(certPEM, keyPEM)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "").WithSAMLConfig(spCfg)
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	providerID := uuid.New()
	// Seed a valid IdP metadata XML — we need it to pass SP construction
	// during the callback handler before ParseResponse fails on the bogus
	// SAMLResponse.
	dummySPMD := dummySPMetadataXML(t)
	idp := newTestIDP(t, dummySPMD, "noop@example.com", "Noop")
	idpMD := idpMetadataXML(t, idp.idp)
	providers.providers[providerID] = &repository.AuthProvider{
		ID:                 providerID,
		TenantID:           tenantID,
		Type:               repository.AuthProviderSAML,
		DisplayName:        "Corp",
		Enabled:            true,
		SAMLIdpMetadataXML: string(idpMD),
		AutoProvision:      true,
		DefaultRole:        "reader",
	}

	// Mint a real relay state so the ConsumeByState step succeeds and we
	// reach ParseResponse. AuthnRequest ID is a dummy here — ParseResponse
	// will fail before checking InResponseTo because the SAMLResponse is
	// bogus.
	relayState, err := sso.CreateSAMLLoginSession(context.Background(), tenantID, providerID, "dummy-authn-req-id", "/x")
	if err != nil {
		t.Fatalf("CreateSAMLLoginSession: %v", err)
	}

	form := url.Values{}
	form.Set("SAMLResponse", "not-a-real-saml-response")
	form.Set("RelayState", relayState)
	resp, err := http.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", form)
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
// SAML SP keypair returns 501 on ACS — the dashboard relies on this status
// to detect "SAML coming soon" without parsing error bodies.
func TestSAMLCallback_NotConfiguredReturns501(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	providers := newFakeProviderRepo()
	sessions := newFakeSessionRepo()
	credKey := make([]byte, 32)
	for i := range credKey {
		credKey[i] = byte(i)
	}
	sso, _ := service.NewSSO(tc.svc, providers, sessions, credKey)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// NO WithSAMLConfig.
	httpH := NewHTTPHandler(tc.svc, tenantID).WithSSO(sso, "")
	httpH.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	httpH.WithSSO(sso, srv.URL)

	providerID := uuid.New()
	form := url.Values{}
	form.Set("SAMLResponse", "x")
	form.Set("RelayState", "y")
	resp, err := http.PostForm(srv.URL+"/auth/saml/"+providerID.String()+"/acs", form)
	if err != nil {
		t.Fatalf("POST acs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("want 501 when SAML not configured, got %d", resp.StatusCode)
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// idpMetadataXML returns the IdP's metadata document as XML bytes. The IdP
// embeds its signing cert + SSO endpoint so the SP can verify Responses.
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
