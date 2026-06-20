// FE-API-034 — SAML route stubs.
//
// SAML support is DEFERRED in this commit. The routes are registered so the
// URLs are reserved (allowing the dashboard to detect "SAML coming soon" via
// a 501 status instead of a 404) and so a future implementation can drop in
// the full crewjam/saml flow without changing the router contract.
//
// Why deferred: integrating github.com/crewjam/saml meaningfully exceeded
// the scope budget for FE-API-034 once the OAuth flow + admin CRUD landed
// — IdP metadata parsing, XML signature validation, AuthnRequest signing,
// and ACS POST handling each carry security gotchas that deserve their own
// review pass. The OAuth surface still unblocks the Sprint 7 SSO button
// targets (Google/GitHub/Microsoft), which is the bulk of the value.
package handler

import "net/http"

// startSAML is a placeholder for the SAML SP-initiated AuthnRequest flow.
func (h *HTTPHandler) startSAML(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOTIMPLEMENTED", "SAML support is coming in a follow-up sprint")
}

// callbackSAML is a placeholder for the SAML ACS POST endpoint.
func (h *HTTPHandler) callbackSAML(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOTIMPLEMENTED", "SAML support is coming in a follow-up sprint")
}
