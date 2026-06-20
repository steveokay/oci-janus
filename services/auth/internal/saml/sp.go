// Package saml builds crewjam/saml ServiceProvider instances from per-tenant
// IdP metadata and the process-wide SP signing keypair.
//
// FE-API-034 — SAML implementation. This package lives outside the handler
// package so the handler stays HTTP-focused and so unit tests can drive the
// SP construction logic in isolation.
//
// Design notes:
//
//   - SP signing cert/key are process-wide (one keypair for the deployment).
//     IdP metadata is per-tenant per-provider — the same SP keypair signs
//     AuthnRequests across all configured SAML providers.
//   - We deliberately do NOT use samlsp.New + samlsp.Middleware because that
//     ships its own cookie-based RequestTracker and SessionProvider that
//     duplicate work already done by our login_sessions table + JWT issuer.
//     Building a bare *saml.ServiceProvider gives us MakeRedirectAuthenticationRequest
//     and ParseResponse without the cookie machinery.
//   - Metadata is parsed on every SP build. A future optimisation could cache
//     parsed metadata per-provider, but for now correctness wins — admin
//     PATCH that updates saml_idp_metadata_xml takes effect on the next
//     request without any cache invalidation.
package saml

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strings"

	crewjamsaml "github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
)

// SPConfig is the process-wide SAML Service Provider configuration. Loaded
// once at startup from SAML_SP_CERT_PATH + SAML_SP_KEY_PATH. nil means SAML
// is not configured and the handler will return 501 for SAML routes.
type SPConfig struct {
	// Certificate is the PEM-decoded X.509 cert presented by the SP when
	// signing AuthnRequests. The IdP uses the embedded public key to verify
	// our request signature.
	Certificate *x509.Certificate
	// Key is the RSA private key paired with Certificate.
	Key *rsa.PrivateKey
}

// LoadSPConfig parses the cert + key PEM bytes into an SPConfig. Returns an
// error if either input is empty or malformed.
//
// Callers pass file contents (not paths) so the package has no filesystem
// dependency — the server package owns reading the files. This keeps the
// SAML package unit-testable without scratch files on disk.
func LoadSPConfig(certPEM, keyPEM []byte) (*SPConfig, error) {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, errors.New("saml: cert and key PEM bytes are required")
	}

	// Decode the cert. Reject anything that isn't a CERTIFICATE PEM block —
	// the IdP cannot verify a signature made with the wrong key kind.
	cBlock, _ := pem.Decode(certPEM)
	if cBlock == nil || cBlock.Type != "CERTIFICATE" {
		return nil, errors.New("saml: cert PEM is not a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(cBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: parse cert: %w", err)
	}

	// Decode the key. Accept both PKCS8 (modern) and PKCS1 (legacy openssl
	// genrsa). Reject EC keys — crewjam/saml only signs with RSA.
	kBlock, _ := pem.Decode(keyPEM)
	if kBlock == nil {
		return nil, errors.New("saml: key PEM is not decodable")
	}

	var key *rsa.PrivateKey
	switch kBlock.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(kBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("saml: parse PKCS1 key: %w", err)
		}
	case "PRIVATE KEY":
		anyKey, perr := x509.ParsePKCS8PrivateKey(kBlock.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("saml: parse PKCS8 key: %w", perr)
		}
		rsaKey, ok := anyKey.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("saml: PKCS8 key is not RSA (got %T)", anyKey)
		}
		key = rsaKey
	default:
		return nil, fmt.Errorf("saml: key PEM block type %q is not RSA PRIVATE KEY or PRIVATE KEY", kBlock.Type)
	}

	return &SPConfig{Certificate: cert, Key: key}, nil
}

// BuildServiceProvider constructs a crewjam/saml ServiceProvider for one
// configured IdP. metadataXML is the raw bytes of the IdP metadata
// document; acsBaseURL is the public origin of registry-auth (e.g.
// "https://registry.example.com") and providerID is the auth_providers row
// id — combined to produce the ACS URL "<base>/auth/saml/<id>/acs".
//
// entityID and audience are optional overrides from the provider row. When
// empty we fall back to the ACS URL's origin as the EntityID (a common
// default that matches what most IdPs expect).
func BuildServiceProvider(spCfg *SPConfig, metadataXML []byte, acsBaseURL, providerID, entityID, audience string) (*crewjamsaml.ServiceProvider, error) {
	if spCfg == nil {
		return nil, errors.New("saml: SP config is nil")
	}
	if len(metadataXML) == 0 {
		return nil, errors.New("saml: IdP metadata XML is empty")
	}

	idpMetadata, err := samlsp.ParseMetadata(metadataXML)
	if err != nil {
		return nil, fmt.Errorf("saml: parse IdP metadata: %w", err)
	}

	acsURL, err := url.Parse(strings.TrimRight(acsBaseURL, "/") + "/auth/saml/" + providerID + "/acs")
	if err != nil {
		return nil, fmt.Errorf("saml: build ACS URL: %w", err)
	}

	// EntityID defaults to the ACS URL origin so the same SP can serve many
	// tenants without per-tenant manual EntityID config. Operators who need
	// a stable EntityID across redeploys (e.g. for IdP allowlists) can set
	// saml_entity_id on the provider row.
	resolvedEntity := strings.TrimSpace(entityID)
	if resolvedEntity == "" {
		// Fall back to {scheme}://{host}/saml/metadata — a conventional SP
		// metadata URL used as the EntityID by most SAML libraries when the
		// admin has not configured one explicitly.
		base, _ := url.Parse(strings.TrimRight(acsBaseURL, "/") + "/auth/saml/metadata")
		resolvedEntity = base.String()
	}

	sp := &crewjamsaml.ServiceProvider{
		EntityID:    resolvedEntity,
		Key:         spCfg.Key,
		Certificate: spCfg.Certificate,
		AcsURL:      *acsURL,
		IDPMetadata: idpMetadata,
		// SignatureMethod requests that AuthnRequests be signed with
		// RSA-SHA256. Empty would leave them unsigned — most enterprise
		// IdPs reject that.
		SignatureMethod: "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256",
		// AuthnNameIDFormat left at the library default ("transient") so
		// the IdP picks the most appropriate format for its directory.
	}

	// Audience is intentionally not pushed onto the ServiceProvider struct —
	// crewjam/saml derives the expected audience from EntityID during
	// ParseResponse. If the admin configured a separate audience, set it as
	// the EntityID instead. This is the documented crewjam/saml convention.
	if strings.TrimSpace(audience) != "" {
		sp.EntityID = strings.TrimSpace(audience)
	}

	return sp, nil
}

// ExtractAttribute walks the assertion's attribute statements and returns
// the value of the first attribute whose Name or FriendlyName matches any of
// the supplied candidate names. Returns "" when no matching attribute is
// found.
//
// SAML attribute names vary wildly by IdP — Microsoft Entra uses URIs like
// "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"
// while Okta/Auth0 often use short names like "email". Callers pass the
// full candidate list (URIs + short names) and we accept the first hit.
func ExtractAttribute(assertion *crewjamsaml.Assertion, candidates ...string) string {
	if assertion == nil {
		return ""
	}
	// Build a small lookup set for O(1) name matching.
	want := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c != "" {
			want[c] = struct{}{}
		}
	}
	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			if _, ok := want[attr.Name]; ok {
				if v := firstAttributeValue(attr.Values); v != "" {
					return v
				}
			}
			if _, ok := want[attr.FriendlyName]; ok {
				if v := firstAttributeValue(attr.Values); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// firstAttributeValue returns the first non-empty string value from a SAML
// attribute. SAML allows multi-valued attributes; we take the first because
// the fields we care about (email, name) are scalar in practice.
func firstAttributeValue(values []crewjamsaml.AttributeValue) string {
	for _, v := range values {
		if v.Value != "" {
			return v.Value
		}
	}
	return ""
}
