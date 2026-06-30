// REDESIGN-001 RM-003 — SSO HTTP handlers (OAuth flow + public provider list).
//
// Three URL families live here:
//
//	GET  /api/v1/auth/providers           — public list of enabled providers
//	GET  /auth/oauth/{provider_id}/start  — kicks off the redirect dance
//	GET  /auth/oauth/{provider_id}/callback — exchanges code → JWT and redirects
//
// The per-tenant admin CRUD routes (/api/v1/admin/auth-providers/...) are
// REMOVED by REDESIGN-001 RM-003. The Review §A1 sso_admin gate flaw is closed
// by removing the surface entirely.
//
// provider_id in URL paths is now a stable string (e.g. "google", "okta_saml")
// rather than a per-tenant UUID.
//
// SAML routes live in saml.go.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ssoHTTPClientTimeout bounds every outbound IdP call (token exchange,
// userinfo). 10 s is generous for slow IdP endpoints without letting a
// hanging request hold the goroutine for minutes.
const ssoHTTPClientTimeout = 10 * time.Second

// ssoHTTPClient is the single configured HTTP client used by the OAuth flow.
// Created once at package init so each callback does not allocate a fresh
// client (and so timeouts are enforced uniformly — CLAUDE.md §13).
var ssoHTTPClient = &http.Client{Timeout: ssoHTTPClientTimeout}

// providerListResponse is the JSON body returned by GET /api/v1/auth/providers.
type providerListResponse struct {
	Providers []providerListItem `json:"providers"`
}

// providerListItem is the public projection of a global_sso_config row. It
// intentionally omits the encrypted client_secret and client_id.
type providerListItem struct {
	ID          string `json:"id"`   // stable string provider_id (e.g. "google")
	Type        string `json:"type"` // kind value (e.g. "oauth_google")
	DisplayName string `json:"display_name"`
	LoginURL    string `json:"login_url"`
}

// RegisterSSO mounts the SSO HTTP routes onto mux. Called from
// HTTPHandler.Register so we keep all routing in one place.
//
// The handler owns its own SSO service so this function panics if the SSO
// service has not been attached via WithSSO — callers must wire it before
// Register.
func (h *HTTPHandler) RegisterSSO(mux *http.ServeMux) {
	if h.sso == nil {
		// SSO is optional in builds without a credential key — emit a noisy
		// debug log so a misconfigured deployment surfaces in tests.
		slog.Warn("SSO routes not registered — auth.WithSSO() was not called")
		return
	}
	// Public provider list — queried by the FE login screen.
	mux.HandleFunc("GET /api/v1/auth/providers", h.listAuthProviders)

	// OAuth redirect dance.
	mux.HandleFunc("GET /auth/oauth/{provider_id}/start", h.startOAuth)
	mux.HandleFunc("GET /auth/oauth/{provider_id}/callback", h.callbackOAuth)

	// SAML — currently returns 501 when SP cert/key are not configured.
	// Routes registered so the URLs are reserved and the dashboard can detect
	// "SAML coming soon" via a status code rather than a 404.
	mux.HandleFunc("GET /auth/saml/{provider_id}/start", h.startSAML)
	mux.HandleFunc("POST /auth/saml/{provider_id}/acs", h.callbackSAML)

	// NOTE: Per REDESIGN-001 RM-003 the admin CRUD routes
	// (GET/POST/PATCH/DELETE /api/v1/admin/auth-providers/...) are intentionally
	// NOT registered here. The per-tenant SSO admin surface has been removed.
}

// ── GET /api/v1/auth/providers ──────────────────────────────────────────────

// listAuthProviders returns the public, enabled-only global provider list.
// Used by the dashboard's sign-in page to render SSO buttons.
//
// The ?tenant_id= query parameter is accepted but ignored for backward
// compatibility — providers are now global, not per-tenant.
func (h *HTTPHandler) listAuthProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.sso.ListEnabledProviders(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "sso: ListEnabledProviders", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to list providers")
		return
	}

	resp := providerListResponse{Providers: make([]providerListItem, 0, len(providers))}
	for _, p := range providers {
		resp.Providers = append(resp.Providers, providerListItem{
			ID:          p.ProviderID,
			Type:        p.Kind,
			DisplayName: p.DisplayName,
			LoginURL:    loginURLForProvider(p),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// loginURLForProvider returns the intra-app path that initiates the redirect
// dance for one provider. SAML providers point at /auth/saml/.../start; OAuth
// at /auth/oauth/.../start. Centralised so the dashboard never builds the
// URL itself.
func loginURLForProvider(p *repository.GlobalSSOProvider) string {
	if p.Kind == string(repository.AuthProviderSAML) {
		return "/auth/saml/" + p.ProviderID + "/start"
	}
	return "/auth/oauth/" + p.ProviderID + "/start"
}

// ── GET /auth/oauth/{provider_id}/start ─────────────────────────────────────

// startOAuth begins the redirect dance: it validates the provider, mints a
// CSRF state + PKCE pair, persists the login session, and 302s the user to
// the IdP's authorize endpoint.
//
// REDESIGN-001 RM-003: {provider_id} is now a stable string (e.g. "google")
// rather than a per-tenant UUID. TenantID is no longer threaded through the
// session.
func (h *HTTPHandler) startOAuth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	if providerID == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid provider_id")
		return
	}

	// Validate ?next= against the open-redirect allowlist before any DB writes.
	nextURL, err := service.SanitizeNextParam(r.URL.Query().Get("next"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid next parameter")
		return
	}
	if nextURL == "" {
		nextURL = "/"
	}

	// Look up the provider so we can reject disabled providers before building
	// the session row.
	p, err := h.sso.LookupProvider(r.Context(), providerID)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso: LookupProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if !p.AuthProviderType().IsOAuth() {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "provider is not an OAuth provider")
		return
	}

	res, err := h.sso.StartLogin(r.Context(), service.StartLoginInput{
		ProviderID: providerID,
		NextURL:    nextURL,
	})
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso: StartLogin", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	authzURL := buildAuthorizeURL(p, h.ssoBaseURL, res.State, res.PKCEChallenge)
	http.Redirect(w, r, authzURL, http.StatusFound)
}

// buildAuthorizeURL composes the provider-specific authorize URL with the
// state, PKCE challenge, and our callback redirect_uri. Endpoints for the
// named providers (Google/GitHub/Microsoft) are hardcoded compile-time
// constants. Generic OIDC uses the issuer URL + /authorize.
func buildAuthorizeURL(p *repository.GlobalSSOProvider, baseURL, state, challenge string) string {
	redirectURI := baseURL + "/auth/oauth/" + p.ProviderID + "/callback"
	v := url.Values{}
	v.Set("client_id", p.OAuthClientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("response_type", "code")
	v.Set("state", state)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")

	switch repository.AuthProviderType(p.Kind) {
	case repository.AuthProviderOAuthGoogle:
		v.Set("scope", strings.Join(defaultScopes(p, "openid", "email", "profile"), " "))
		// access_type=online: we do not need refresh tokens — short JWT TTL.
		v.Set("access_type", "online")
		return "https://accounts.google.com/o/oauth2/v2/auth?" + v.Encode()
	case repository.AuthProviderOAuthGitHub:
		// GitHub uses a different scope vocabulary; "user:email" lets us
		// read the verified email even when the user has hidden it from
		// their profile.
		v.Set("scope", strings.Join(defaultScopes(p, "read:user", "user:email"), " "))
		return "https://github.com/login/oauth/authorize?" + v.Encode()
	case repository.AuthProviderOAuthMicrosoft:
		v.Set("scope", strings.Join(defaultScopes(p, "openid", "email", "profile", "User.Read"), " "))
		return "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?" + v.Encode()
	case repository.AuthProviderOAuthGeneric:
		v.Set("scope", strings.Join(defaultScopes(p, "openid", "email", "profile"), " "))
		base := strings.TrimRight(p.OAuthIssuerURL, "/")
		return base + "/authorize?" + v.Encode()
	}
	return ""
}

// defaultScopes returns the persisted scope list, falling back to the
// caller-supplied defaults when the row was created with an empty array.
func defaultScopes(p *repository.GlobalSSOProvider, fallback ...string) []string {
	if len(p.OAuthScopes) > 0 {
		return p.OAuthScopes
	}
	return fallback
}

// ── GET /auth/oauth/{provider_id}/callback ──────────────────────────────────

// callbackOAuth completes the redirect dance: validate state, exchange code
// for tokens, call userinfo, ensure-or-provision a user, issue a JWT, and
// redirect the browser back to the SPA with the token in a query param.
//
// JWT-return mechanism: 302 to {next}?sso_token=<jwt>. The frontend swaps
// the token into its authStore on first paint and immediately removes it
// from the URL via history.replaceState. The trade-off is documented in
// the FE-API-034 commit body: the JWT briefly appears in browser history,
// which is acceptable given the 5-minute token TTL.
func (h *HTTPHandler) callbackOAuth(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	if providerID == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid provider_id")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "state and code are required")
		return
	}

	// Consume the session FIRST so a replay of the same state immediately
	// fails (ConsumeByState deletes the row atomically).
	sess, err := h.sso.ConsumeLoginSession(r.Context(), state)
	if err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "state expired or already used")
			return
		}
		slog.ErrorContext(r.Context(), "sso: ConsumeLoginSession", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Defence in depth: session's provider_id must match the URL.
	if sess.ProviderID != providerID {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "provider mismatch")
		return
	}

	// Load the provider WITH the encrypted secret for the token exchange.
	p, clientSecret, err := h.sso.LookupProviderWithSecret(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
		return
	}

	redirectURI := h.ssoBaseURL + "/auth/oauth/" + providerID + "/callback"
	tokens, err := exchangeOAuthCode(r.Context(), p, redirectURI, code, sess.PKCEVerifier, clientSecret)
	if err != nil {
		slog.ErrorContext(r.Context(), "sso: token exchange failed", "err", err, "provider_id", providerID)
		writeError(w, http.StatusBadGateway, "BADGATEWAY", "token exchange failed")
		return
	}

	ident, err := fetchUserInfo(r.Context(), p, tokens)
	if err != nil {
		slog.ErrorContext(r.Context(), "sso: userinfo fetch failed", "err", err, "provider_id", providerID)
		writeError(w, http.StatusBadGateway, "BADGATEWAY", "userinfo fetch failed")
		return
	}

	// RM-004: TenantID is no longer in the session. EnsureSSOUser resolves
	// the tenant from the user row or falls back to s.defaultTenantID.
	user, roles, err := h.sso.EnsureSSOUser(r.Context(), p, ident, h.devDefaultTenant)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrEmailNotVerified):
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "email is not verified at the identity provider")
			return
		case errors.Is(err, service.ErrAutoProvisionDisabled):
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user does not exist and auto-provision is disabled")
			return
		case errors.Is(err, service.ErrAccountDisabled):
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "account is disabled")
			return
		case errors.Is(err, service.ErrSSOSubjectMismatch):
			// SEC-043 — explicit dispatch so the SEC-042 generic body
			// reaches the wire. Without this branch the mismatch fell
			// through to the default 500 INTERNAL response and the
			// generic phrasing built by EnsureSSOUser was never
			// rendered to the user.
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "this SSO identity is not linked to a registered account — contact your admin to link it")
			return
		}
		slog.ErrorContext(r.Context(), "sso: EnsureSSOUser", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	tok, err := h.sso.IssueSSOToken(r.Context(), user, roles)
	if err != nil {
		slog.ErrorContext(r.Context(), "sso: IssueSSOToken", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Compose the SPA redirect with the JWT. SanitizeNextParam at /start
	// guarantees sess.RedirectURL is a safe intra-app path; we still
	// re-parse defensively in case the row was tampered with at rest.
	dest, perr := safeAppendQuery(sess.RedirectURL, "sso_token", tok)
	if perr != nil {
		dest = "/?sso_token=" + url.QueryEscape(tok)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// safeAppendQuery appends ?k=v to base, validating that base is an
// intra-app path. Returns an error if the path is malformed.
func safeAppendQuery(base, k, v string) (string, error) {
	if base == "" || !strings.HasPrefix(base, "/") || strings.HasPrefix(base, "//") {
		return "", fmt.Errorf("invalid base path")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", fmt.Errorf("invalid base path")
	}
	q := u.Query()
	q.Set(k, v)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ── Outbound IdP calls ──────────────────────────────────────────────────────

// oauthTokens is the canonical token-exchange response shape across the
// providers we support. We only need the access token; id_token is parsed
// for Google/Microsoft to read email_verified.
type oauthTokens struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

// exchangeOAuthCode POSTs to the IdP's token endpoint. PKCE verifier
// authenticates the request without exposing client_secret to the browser
// (the secret is still sent here as an additional confidential-client
// authentication factor for Google/Microsoft; GitHub allows both).
func exchangeOAuthCode(ctx context.Context, p *repository.GlobalSSOProvider, redirectURI, code, verifier, clientSecret string) (*oauthTokens, error) {
	tokenURL := tokenEndpoint(p)
	if tokenURL == "" {
		return nil, fmt.Errorf("no token endpoint for provider kind %s", p.Kind)
	}
	form := url.Values{}
	form.Set("client_id", p.OAuthClientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub requires Accept: application/json or it returns form-encoded.
	req.Header.Set("Accept", "application/json")

	resp, err := ssoHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, truncateBody(body))
	}
	var tokens oauthTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &tokens, nil
}

// tokenEndpoint returns the token endpoint URL for the provider. Hardcoded
// for the named providers; for generic OIDC the issuer URL + /token is
// assumed (full discovery is left for follow-up work).
func tokenEndpoint(p *repository.GlobalSSOProvider) string {
	switch repository.AuthProviderType(p.Kind) {
	case repository.AuthProviderOAuthGoogle:
		return "https://oauth2.googleapis.com/token"
	case repository.AuthProviderOAuthGitHub:
		return "https://github.com/login/oauth/access_token"
	case repository.AuthProviderOAuthMicrosoft:
		return "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	case repository.AuthProviderOAuthGeneric:
		return strings.TrimRight(p.OAuthIssuerURL, "/") + "/token"
	}
	return ""
}

// fetchUserInfo calls the IdP's userinfo endpoint with the access token and
// returns the normalised identity. The verified-email semantics differ by
// provider so each branch has its own mapping.
func fetchUserInfo(ctx context.Context, p *repository.GlobalSSOProvider, tokens *oauthTokens) (service.SSOIdentity, error) {
	endpoint := userInfoEndpoint(p)
	if endpoint == "" {
		return service.SSOIdentity{}, fmt.Errorf("no userinfo endpoint for provider kind %s", p.Kind)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return service.SSOIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := ssoHTTPClient.Do(req)
	if err != nil {
		return service.SSOIdentity{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return service.SSOIdentity{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return service.SSOIdentity{}, fmt.Errorf("userinfo %d: %s", resp.StatusCode, truncateBody(body))
	}

	switch repository.AuthProviderType(p.Kind) {
	case repository.AuthProviderOAuthGoogle, repository.AuthProviderOAuthMicrosoft, repository.AuthProviderOAuthGeneric:
		// Standard OIDC userinfo payload — email_verified is a boolean.
		var u struct {
			Sub           string `json:"sub"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
		}
		if err := json.Unmarshal(body, &u); err != nil {
			return service.SSOIdentity{}, fmt.Errorf("decode userinfo: %w", err)
		}
		// Microsoft Graph (User.Read) returns "mail" or "userPrincipalName"
		// instead of "email"; try those if email is empty.
		if u.Email == "" {
			var m struct {
				Mail string `json:"mail"`
				UPN  string `json:"userPrincipalName"`
				DN   string `json:"displayName"`
			}
			_ = json.Unmarshal(body, &m)
			if m.Mail != "" {
				u.Email = m.Mail
			} else if m.UPN != "" {
				u.Email = m.UPN
			}
			if u.Name == "" && m.DN != "" {
				u.Name = m.DN
			}
			// Graph does not expose email_verified — trust the IdP (the user
			// authenticated against MS Entra ID).
			if repository.AuthProviderType(p.Kind) == repository.AuthProviderOAuthMicrosoft && u.Email != "" {
				u.EmailVerified = true
			}
		}
		return service.SSOIdentity{
			Email:         u.Email,
			EmailVerified: u.EmailVerified,
			DisplayName:   u.Name,
			Subject:       u.Sub,
		}, nil

	case repository.AuthProviderOAuthGitHub:
		// GitHub /user does NOT include a verified flag for the primary email.
		// Fall back to /user/emails, which returns an array with verified flags.
		var u struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"` // may be null if user hides it
		}
		if err := json.Unmarshal(body, &u); err != nil {
			return service.SSOIdentity{}, fmt.Errorf("decode github user: %w", err)
		}

		email, verified, err := githubVerifiedPrimaryEmail(ctx, tokens.AccessToken)
		if err == nil && email != "" {
			u.Email = email
		}
		return service.SSOIdentity{
			Email:         u.Email,
			EmailVerified: verified,
			DisplayName:   u.Name,
			Subject:       fmt.Sprintf("%d", u.ID),
		}, nil
	}
	return service.SSOIdentity{}, fmt.Errorf("unsupported provider kind: %s", p.Kind)
}

// userInfoEndpoint returns the userinfo URL for the provider. For Microsoft
// we use Graph's /me (the v2 endpoint exposes a small userinfo too but Graph
// is the canonical path for Entra ID).
func userInfoEndpoint(p *repository.GlobalSSOProvider) string {
	switch repository.AuthProviderType(p.Kind) {
	case repository.AuthProviderOAuthGoogle:
		return "https://openidconnect.googleapis.com/v1/userinfo"
	case repository.AuthProviderOAuthGitHub:
		return "https://api.github.com/user"
	case repository.AuthProviderOAuthMicrosoft:
		return "https://graph.microsoft.com/v1.0/me"
	case repository.AuthProviderOAuthGeneric:
		return strings.TrimRight(p.OAuthIssuerURL, "/") + "/userinfo"
	}
	return ""
}

// githubVerifiedPrimaryEmail calls /user/emails and returns the primary
// verified email. If the user has no verified primary email we return
// ("", false, nil) so the caller falls back to the /user email field.
func githubVerifiedPrimaryEmail(ctx context.Context, accessToken string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := ssoHTTPClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("github emails %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(body, &emails); err != nil {
		return "", false, err
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, true, nil
		}
	}
	// Try any verified email as a fallback so the user can still sign in
	// when they haven't marked a primary.
	for _, e := range emails {
		if e.Verified {
			return e.Email, true, nil
		}
	}
	return "", false, nil
}

// truncateBody returns at most 256 bytes of a remote error body so the log
// line stays short. Always avoid logging full IdP response bodies — they may
// contain refresh tokens or other secrets.
func truncateBody(b []byte) string {
	if len(b) > 256 {
		return string(b[:256]) + "..."
	}
	return string(b)
}
