// REDESIGN-001 RM-003 — SAML SP-initiated authentication flow.
//
// Two routes:
//
//	GET  /auth/saml/{provider_id}/start  — mints the AuthnRequest + redirects
//	POST /auth/saml/{provider_id}/acs    — receives the IdP SAMLResponse
//
// Both routes return 501 NOT_CONFIGURED when SAML support is not wired
// (SAML_SP_CERT_PATH / SAML_SP_KEY_PATH unset). When wired, the start handler
// reuses the same auth_login_sessions table that powers OAuth — the RelayState
// SAML term is the same concept as the OAuth `state` token (single-use,
// 10-minute TTL, originated by us). The ACS handler consumes that session,
// validates the IdP signature + Conditions via crewjam/saml, extracts the
// user identifier, and reuses EnsureSSOUser + IssueSSOToken so the
// auto-provisioning path is identical to OAuth.
//
// Changes from FE-API-034 (REDESIGN-001 RM-003):
//   - {provider_id} in URL is now a stable string (e.g. "okta_saml") not a UUID.
//   - TenantID is no longer threaded through the SAML session (RM-004).
//   - GetProvider → LookupProvider (string-keyed, global).
//   - CreateSAMLLoginSession: tenantID + providerID UUID args removed.
package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	crewjamsaml "github.com/crewjam/saml"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// SAML binding URNs. Re-declared locally so the handler doesn't need to
// import crewjam/saml just for these constants; the underlying URNs are
// fixed by the SAML 2.0 spec and won't change.
const (
	crewjamSAMLHTTPRedirectBinding = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	crewjamSAMLHTTPPostBinding     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
)

// SAML attribute name candidates we walk in priority order to recover the
// user's email address. Most enterprise IdPs publish a long URN-style name;
// Okta/Auth0 often add a short alias. The first matching attribute wins; if
// none match we fall back to the assertion's NameID (which IdPs frequently
// populate with the user's email when Format is emailAddress).
var samlEmailAttributes = []string{
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	"urn:oid:0.9.2342.19200300.100.1.3",
	"email",
	"mail",
	"emailAddress",
	"EmailAddress",
}

// SAML attribute name candidates for the user's display name. Same priority
// ordering as samlEmailAttributes — URN first, short name second.
var samlNameAttributes = []string{
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/displayname",
	"urn:oid:2.16.840.1.113730.3.1.241",
	"urn:oid:2.5.4.3",
	"name",
	"displayName",
	"DisplayName",
	"cn",
}

// startSAML mints an AuthnRequest, persists the login session, and 302s the
// user to the IdP's SSO endpoint via the HTTP-Redirect binding.
func (h *HTTPHandler) startSAML(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	if providerID == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid provider_id")
		return
	}

	// Validate ?next= against the open-redirect allowlist BEFORE any DB work.
	nextURL, err := service.SanitizeNextParam(r.URL.Query().Get("next"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid next parameter")
		return
	}
	if nextURL == "" {
		nextURL = "/"
	}

	// SAML config check happens after path-param + next validation so the
	// 501 is reserved for "we'd have served it but the deployment isn't
	// configured" rather than masking bad-request bugs.
	if h.samlConfig == nil {
		writeError(w, http.StatusNotImplemented, "NOTCONFIGURED", "SAML SP cert/key not configured")
		return
	}

	p, err := h.sso.LookupProvider(r.Context(), providerID)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "saml: LookupProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if p.Kind != string(repository.AuthProviderSAML) {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "provider is not a SAML provider")
		return
	}

	// saml_metadata_xml is stored as BYTEA; convert to []byte for BuildServiceProvider.
	sp, err := saml.BuildServiceProvider(
		h.samlConfig,
		p.SAMLMetadataXML,
		h.ssoBaseURL,
		p.ProviderID,
		"", // entity_id override — not stored in global_sso_config (follow-up)
		"", // audience override — not stored in global_sso_config (follow-up)
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: BuildServiceProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "SAML configuration error")
		return
	}

	// Build the AuthnRequest directly so we can capture its generated ID. The
	// ID is persisted alongside the RelayState so callbackSAML can pass it to
	// ParseResponse as the only permitted InResponseTo value — this blocks an
	// attacker from injecting a response from a different SP-initiated flow
	// even if they capture our RelayState.
	authnReq, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(crewjamSAMLHTTPRedirectBinding),
		crewjamSAMLHTTPRedirectBinding,
		crewjamSAMLHTTPPostBinding,
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: MakeAuthenticationRequest", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not build AuthnRequest")
		return
	}

	// Mint a single-use RelayState — same shape as the OAuth `state` token
	// (32 random bytes, base64url). RM-003: string providerID. RM-004: no tenantID.
	relayState, err := h.sso.CreateSAMLLoginSession(r.Context(), p.ProviderID, authnReq.ID, nextURL)
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: CreateSAMLLoginSession", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not start SAML session")
		return
	}

	authnURL, err := authnReq.Redirect(relayState, sp)
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: AuthnRequest.Redirect", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not build AuthnRequest URL")
		return
	}

	http.Redirect(w, r, authnURL.String(), http.StatusFound)
}

// callbackSAML handles the ACS POST from the IdP. crewjam/saml's
// ParseResponse validates the signature, Conditions (NotOnOrAfter), audience,
// and Recipient — anything failing those checks bubbles up as an error and
// becomes a 400 INVALIDSAML response.
func (h *HTTPHandler) callbackSAML(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	if providerID == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid provider_id")
		return
	}

	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid form body")
		return
	}
	samlResponse := r.PostForm.Get("SAMLResponse")
	relayState := r.PostForm.Get("RelayState")

	if samlResponse == "" || relayState == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "missing SAMLResponse or RelayState")
		return
	}

	if h.samlConfig == nil {
		writeError(w, http.StatusNotImplemented, "NOTCONFIGURED", "SAML SP cert/key not configured")
		return
	}

	p, err := h.sso.LookupProvider(r.Context(), providerID)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "saml: LookupProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if p.Kind != string(repository.AuthProviderSAML) {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "provider is not a SAML provider")
		return
	}

	// Consume the login session FIRST so a replayed RelayState fails the
	// second request even before we touch the SAMLResponse. Single-use is
	// our primary defence — XML signature validity says nothing about
	// whether the IdP message has already been delivered.
	sess, err := h.sso.ConsumeLoginSession(r.Context(), relayState)
	if err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			writeError(w, http.StatusBadRequest, "INVALIDSTATE", "invalid or expired RelayState")
			return
		}
		slog.ErrorContext(r.Context(), "saml: ConsumeLoginSession", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if sess.ProviderID != providerID {
		writeError(w, http.StatusBadRequest, "INVALIDSTATE", "RelayState does not match provider")
		return
	}

	sp, err := saml.BuildServiceProvider(
		h.samlConfig,
		p.SAMLMetadataXML,
		h.ssoBaseURL,
		p.ProviderID,
		"", // entity_id override
		"", // audience override
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: BuildServiceProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "SAML configuration error")
		return
	}

	// ParseResponse validates the XML signature, conditions, audience, and
	// the InResponseTo attribute. The possibleRequestIDs slice tells
	// crewjam/saml which AuthnRequest IDs we have outstanding — we pass the
	// ID we stashed in sess.PKCEVerifier at /start, so only the specific
	// in-flight authentication request can be answered. This is stronger
	// than the RelayState check alone: without it, a stolen RelayState could
	// be paired with an attacker-induced response from the same IdP for a
	// different SP-initiated flow.
	assertion, err := sp.ParseResponse(r, []string{sess.PKCEVerifier})
	if err != nil {
		// Don't echo the underlying error string back to the user — it can
		// leak details about our SP config or the IdP cert. Log full detail
		// server-side; return a generic 400.
		privErr := err
		if invalid, ok := err.(*crewjamsaml.InvalidResponseError); ok && invalid.PrivateErr != nil {
			privErr = invalid.PrivateErr
		}
		slog.WarnContext(r.Context(), "saml: ParseResponse failed",
			"err", err.Error(),
			"cause", privErr.Error(),
			"provider_id", providerID)
		writeError(w, http.StatusBadRequest, "INVALIDSAML", "SAML response failed validation")
		return
	}

	email := saml.ExtractAttribute(assertion, samlEmailAttributes...)
	name := saml.ExtractAttribute(assertion, samlNameAttributes...)

	// Fall back to NameID when the assertion didn't include a separate email
	// attribute — many IdPs populate NameID with the user's email when
	// Format is emailAddress.
	if email == "" {
		if assertion.Subject != nil && assertion.Subject.NameID != nil {
			email = strings.TrimSpace(assertion.Subject.NameID.Value)
		}
	}
	if email == "" {
		writeError(w, http.StatusBadRequest, "MISSINGEMAIL", "SAML assertion has no email")
		return
	}

	// REDESIGN-001 Phase 5.6 — SAML email-trust flag.
	//
	// SAML 2.0 doesn't carry a standard `email_verified` attribute. Whether
	// the IdP actually verified the asserted email before issuing the
	// assertion is deployment-specific: Okta / Azure AD / Google Workspace /
	// Auth0 typically guarantee it; raw ADFS and many custom IdPs do not.
	// SSO_SAML_TRUST_EMAIL lets the operator opt into trust after auditing
	// their IdP — the default is false (fail-safe).
	//
	// When the flag is false, EnsureSSOUser will return ErrEmailNotVerified
	// (its existing OAuth-path gate) and we surface a clear 403. This blocks
	// both new auto-provisioning AND existing-user login because we can't
	// distinguish the two without trusting the assertion's email in the
	// first place. The follow-up is a one-time email-verification flow on
	// the SAML provisioning path so unverified assertions can still be
	// accepted safely.
	//
	// TODO(REDESIGN-001 Phase 5.6 follow-up): when SSO_SAML_TRUST_EMAIL=false
	// and the assertion is otherwise valid, route the user through a one-time
	// email-verification step (send a token to ident.Email, gate JWT issuance
	// on confirmation) instead of refusing the request. Tracked separately
	// because it requires a verification-token table + email transport
	// plumbing that's out of scope for the trust-flag flip itself.
	//
	// RM-004: devDefaultTenant is passed as the fallback; EnsureSSOUser
	// resolves the final tenant from the user row or s.defaultTenantID.
	user, roles, err := h.sso.EnsureSSOUser(r.Context(), p, service.SSOIdentity{
		Email:         email,
		EmailVerified: h.samlTrustEmail,
		DisplayName:   name,
		Subject:       samlSubject(assertion),
	}, h.devDefaultTenant)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAutoProvisionDisabled):
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user does not exist and auto-provision is disabled")
			return
		case errors.Is(err, service.ErrAccountDisabled):
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "account is disabled")
			return
		case errors.Is(err, service.ErrEmailNotVerified):
			// REDESIGN-001 Phase 5.6 — SSO_SAML_TRUST_EMAIL is false. Log at
			// warn so operators can see the rejection in the dashboard and
			// decide whether to flip the flag after auditing their IdP.
			slog.WarnContext(r.Context(), "saml: refusing login because SSO_SAML_TRUST_EMAIL=false; set it to true after confirming your IdP verifies emails before asserting them, or wait for the post-login email-verification flow follow-up",
				"provider_id", providerID,
				"email", email)
			writeError(w, http.StatusForbidden, "EMAILNOTVERIFIED", "SAML email cannot be trusted on this deployment; ask your administrator to set SSO_SAML_TRUST_EMAIL=true after confirming the IdP verifies emails")
			return
		case errors.Is(err, service.ErrSSOSubjectMismatch):
			// SEC-043 — explicit dispatch so the SEC-042 generic body
			// reaches the wire. Matches the OAuth branch in sso.go to
			// keep the rejection shape uniform across both SSO flows.
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "this SSO identity is not linked to a registered account — contact your admin to link it")
			return
		}
		slog.ErrorContext(r.Context(), "saml: EnsureSSOUser", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not complete login")
		return
	}

	// Capture the client IP + User-Agent so the SAML login creates a
	// listable/revocable session row (the active-session-list feature).
	tok, err := h.sso.IssueSSOToken(r.Context(), user, roles,
		service.SessionMeta{IP: remoteIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		slog.ErrorContext(r.Context(), "saml: IssueSSOToken", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not issue token")
		return
	}

	// Same JWT return mechanism as OAuth: 302 to {next}?sso_token=<jwt>.
	dest, perr := safeAppendQuery(sess.RedirectURL, "sso_token", tok)
	if perr != nil {
		dest = "/?sso_token=" + url.QueryEscape(tok)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// samlSubject returns the assertion's NameID value (or "" when missing). Used
// as the audit-friendly subject identifier when logging SSO provisioning;
// passed into SSOIdentity.Subject so the auth.user_sso_provisioned event
// carries something stable per-user across re-logins.
func samlSubject(assertion *crewjamsaml.Assertion) string {
	if assertion == nil || assertion.Subject == nil || assertion.Subject.NameID == nil {
		return ""
	}
	return strings.TrimSpace(assertion.Subject.NameID.Value)
}
