package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// trustedProxyCIDRs holds the parsed CIDR ranges that are allowed to set
// X-Forwarded-For. An empty slice means no proxy is trusted and RemoteAddr
// is always used (CLAUDE.md §7 SEC-009). Server.go populates this once at
// startup via SetTrustedProxies.
var trustedProxyCIDRs []*net.IPNet

// ParseTrustedProxyCIDRs splits a comma-separated value (e.g. the env var
// TRUSTED_PROXY_CIDRS) into a slice of *net.IPNet. Malformed CIDR entries
// are logged and skipped so a misconfigured value degrades gracefully
// rather than blocking startup.
//
// QA-006: env reads moved out of an init() in this handler package.
// Config + env access belong in the loader layer; server.go calls this
// with the validated config value and passes the result to
// SetTrustedProxies. Package-level state remains so the existing
// remoteIP-test save/restore pattern keeps working.
func ParseTrustedProxyCIDRs(raw string) []*net.IPNet {
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, cidr := range strings.Split(raw, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Warn("TRUSTED_PROXY_CIDRS: invalid CIDR skipped", "entry", cidr, "error", err)
			continue
		}
		out = append(out, network)
	}
	return out
}

// SetTrustedProxies replaces the package-level trusted-proxy list. Intended
// to be called once at server startup from server.go after the config is
// loaded. Subsequent calls overwrite (useful in tests).
func SetTrustedProxies(cidrs []*net.IPNet) {
	trustedProxyCIDRs = cidrs
}

// isTrustedProxy reports whether ip falls within one of the configured
// trusted proxy CIDRs. Returns false when trustedProxyCIDRs is empty
// (no proxy trust).
func isTrustedProxy(ip net.IP) bool {
	for _, cidr := range trustedProxyCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// HTTPHandler implements the HTTP API for registry-auth.
type HTTPHandler struct {
	svc              *service.Service
	devDefaultTenant uuid.UUID // zero when not set; only used in local dev
	// sso is the optional SSO sub-service (FE-API-034). nil disables the SSO
	// routes; set via WithSSO.
	sso *service.SSO
	// ssoBaseURL is the public origin used to build the OAuth redirect_uri
	// (must match what the IdP has registered). Configured via SSO_BASE_URL.
	ssoBaseURL string
	// samlConfig is the optional SAML SP signing keypair (FE-API-034). nil
	// disables SAML support — the /auth/saml/... routes return 501.
	samlConfig *saml.SPConfig
	// samlTrustEmail controls whether the SAML ACS handler treats the
	// IdP-asserted email as already verified (REDESIGN-001 Phase 5.6).
	// Default false (fail-safe); operators opt in via SSO_SAML_TRUST_EMAIL=true
	// after confirming their IdP verifies emails before asserting them. When
	// false, the SAML ACS handler refuses to provision new users and refuses
	// to log in existing users, because every assertion email is treated as
	// untrusted until the post-login email-verification flow ships.
	samlTrustEmail bool
	// saService is the optional ServiceAccountService (FE-API-048, T13). nil
	// causes all /api/v1/service-accounts routes to return 501 NOT_IMPLEMENTED.
	// Set via WithServiceAccountService.
	saService *service.ServiceAccountService
	// activityService is the optional ActivityService (FE-API-048, T15). nil
	// causes GET /api/v1/access/activity to return 501 NOT_IMPLEMENTED.
	// Set via WithActivityService.
	activityService *service.ActivityService
	// oidc is the FUT-001 trust + workload-token-exchange service. nil
	// causes POST /auth/token/workload to return 503 with a clear
	// "feature off" message. Set via WithWorkloadExchange.
	oidc *service.OIDCTrustService
	// workloadRedis backs the per-(issuer, subject) Redis rate-limit on
	// /auth/token/workload. nil disables rate-limiting; the exchange
	// still works (fail-OPEN). Set via WithWorkloadExchange.
	workloadRedis *redis.Client
}

// NewHTTPHandler creates an HTTPHandler backed by the given service.
// devDefaultTenantID may be uuid.Nil (production) or a fixed dev UUID.
//
// A warning is logged when no trusted proxies are configured because
// IP-based rate limiting will then target the TCP peer (the proxy)
// rather than the actual client, rendering per-client rate limits
// ineffective in deployed environments where traffic arrives through a
// load balancer or ingress proxy. SetTrustedProxies must have been
// called before this constructor for the warning to read the populated
// value (server.go does this).
func NewHTTPHandler(svc *service.Service, devDefaultTenantID uuid.UUID) *HTTPHandler {
	if len(trustedProxyCIDRs) == 0 {
		slog.Warn("TRUSTED_PROXY_CIDRS not set — IP rate limiting uses TCP peer address (degraded when behind a proxy)")
	}
	return &HTTPHandler{svc: svc, devDefaultTenant: devDefaultTenantID}
}

// WithSSO attaches the SSO sub-service and the base URL used to build OAuth
// redirect URIs. Returns the handler so callers can chain. Optional —
// without WithSSO the SSO routes are never registered.
func (h *HTTPHandler) WithSSO(sso *service.SSO, baseURL string) *HTTPHandler {
	h.sso = sso
	h.ssoBaseURL = strings.TrimRight(baseURL, "/")
	return h
}

// WithSAMLConfig attaches the SP signing keypair used by the SAML routes.
// Optional — without it the /auth/saml/... routes return 501. Returns the
// handler so callers can chain.
func (h *HTTPHandler) WithSAMLConfig(cfg *saml.SPConfig) *HTTPHandler {
	h.samlConfig = cfg
	return h
}

// WithSAMLTrustEmail wires the SSO_SAML_TRUST_EMAIL flag into the SAML ACS
// handler (REDESIGN-001 Phase 5.6). Defaults to false (fail-safe) when never
// called. Returns the handler so callers can chain.
func (h *HTTPHandler) WithSAMLTrustEmail(trust bool) *HTTPHandler {
	h.samlTrustEmail = trust
	return h
}

// WithServiceAccountService attaches the ServiceAccountService used by the
// /api/v1/service-accounts routes (FE-API-048, T13). Optional — without it
// those routes return 501 NOT_IMPLEMENTED. Returns the handler for chaining.
func (h *HTTPHandler) WithServiceAccountService(svc *service.ServiceAccountService) *HTTPHandler {
	h.saService = svc
	return h
}

// WithActivityService attaches the ActivityService used by the
// GET /api/v1/access/activity route (FE-API-048, T15). Optional — without it
// the route returns 501 NOT_IMPLEMENTED. Returns the handler for chaining.
func (h *HTTPHandler) WithActivityService(svc *service.ActivityService) *HTTPHandler {
	h.activityService = svc
	return h
}

// Register mounts all auth routes onto mux.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	// Docker token auth — RFC 7235 flow; Docker clients use GET, some tools use POST.
	mux.HandleFunc("POST /auth/token", h.token)
	mux.HandleFunc("GET /auth/token", h.token)
	mux.HandleFunc("GET /.well-known/jwks.json", h.jwks)
	// FUT-001 — federated workload identity exchange. Public (the OIDC
	// JWT itself is the credential), gated by per-(iss, sub) Redis
	// rate-limit and the deploy-time OIDC_ALLOWED_ISSUERS allowlist.
	mux.HandleFunc("POST /auth/token/workload", h.HandleWorkloadTokenExchange)
	mux.HandleFunc("POST /api/v1/users", h.createUser)
	mux.HandleFunc("POST /api/v1/login", h.login)
	// Tier-1 #1 — step 2 of two-step login: exchange an mfa_challenge token +
	// OTP/backup code for a full access token. Public (the challenge token is
	// the credential); shares the login handler's per-IP rate-limit + auth-
	// failure recording so the OTP step is brute-force-bounded like /login.
	mux.HandleFunc("POST /api/v1/login/mfa", h.loginMFA)
	mux.HandleFunc("POST /api/v1/logout", h.logout)
	mux.HandleFunc("POST /api/v1/token/refresh", h.refreshToken)
	mux.HandleFunc("POST /api/v1/apikeys", h.createAPIKey)
	mux.HandleFunc("GET /api/v1/apikeys", h.listAPIKeys)
	mux.HandleFunc("DELETE /api/v1/apikeys/{id}", h.deleteAPIKey)
	// FE-API-011 / FE-API-012 / FE-API-013 — current-user profile & password.
	mux.HandleFunc("GET /api/v1/users/me", h.getCurrentUser)
	mux.HandleFunc("PATCH /api/v1/users/me", h.updateCurrentUser)
	mux.HandleFunc("POST /api/v1/users/me/password", h.changeCurrentUserPassword)
	// REDESIGN-001 Phase 4.3 — post-login onboarding wizard completion.
	// Flips users.onboarding_complete = true for the authenticated human user.
	// Idempotent; service accounts get a 403.
	mux.HandleFunc("POST /api/v1/users/me/onboarding/complete", h.completeOnboarding)
	// Tier-1 #1 — TOTP MFA self-service (identity from the Bearer token).
	// enroll + verify additionally accept a short-lived mfa_setup token so a
	// require-MFA-gated user can enrol before holding an access token.
	mux.HandleFunc("GET /api/v1/users/me/mfa", h.mfaStatus)
	mux.HandleFunc("POST /api/v1/users/me/mfa/enroll", h.mfaEnroll)
	mux.HandleFunc("POST /api/v1/users/me/mfa/verify", h.mfaVerify)
	mux.HandleFunc("DELETE /api/v1/users/me/mfa", h.mfaDisable)
	mux.HandleFunc("POST /api/v1/users/me/mfa/backup-codes/regenerate", h.mfaRegenerateBackupCodes)
	// FE-API-034 — SSO providers + OAuth flow + admin CRUD. The RegisterSSO
	// call no-ops when WithSSO() was not invoked, so dev deployments without
	// SSO_CREDENTIAL_KEY simply skip these routes.
	h.RegisterSSO(mux)
	// FE-API-048 — service-account CRUD. Always registered; individual routes
	// return 501 when saService is nil (WithServiceAccountService not called).
	h.RegisterServiceAccounts(mux)
	// FE-API-048 T15 — access activity feed. Always registered; returns 501
	// when activityService is nil (WithActivityService not called).
	h.RegisterAccessActivity(mux)
}

// ── Docker token endpoint ─────────────────────────────────────────────────────

// token implements the Docker Registry Token Auth flow.
// Accepts Basic credentials (username:password or keyUUID:rawSecret) and
// returns a scoped JWT valid for 300 s.
func (h *HTTPHandler) token(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if err := h.svc.CheckIPRateLimit(r.Context(), ip); err != nil {
		writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "rate limit exceeded")
		return
	}

	tenantID, err := h.parseTenantID(r)
	if err != nil {
		w.Header().Set("Www-Authenticate", `Bearer realm="/auth/token",service="registry"`)
		writeError(w, http.StatusBadRequest, "BADREQUEST", "missing or invalid X-Tenant-ID header")
		return
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("Www-Authenticate", `Bearer realm="/auth/token",service="registry"`)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	scopes := r.URL.Query()["scope"]
	access := parseScopes(scopes)

	var userID, userTenantID, principalKind string

	// If username is a valid UUID, treat it as an API key ID.
	if keyID, parseErr := uuid.Parse(username); parseErr == nil {
		vk, err := h.svc.ValidateAPIKey(r.Context(), service.ValidateAPIKeyOpts{
			KeyID:     keyID,
			RawSecret: password,
			// RequestTenantID is intentionally nil here: the Docker /auth/token
			// endpoint derives the tenant from the Basic Auth tenant header, not
			// from X-Tenant-ID. The cross-tenant guard via RequestTenantID is
			// applied only through the gRPC ValidateAPIKey path (T13).
		})
		if err != nil {
			h.svc.RecordAuthFailure(r.Context(), ip)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			return
		}
		userID = vk.UserID.String()
		userTenantID = vk.TenantID.String()
		// vk.PrincipalKind ("human" or "service_account") flows into the issued
		// JWT so downstream services can deny SA principals at admin gates
		// without re-querying the user record (REDESIGN-001 Phase 5.4).
		principalKind = vk.PrincipalKind
	} else {
		user, err := h.svc.AuthenticateUser(r.Context(), tenantID, username, password)
		if err != nil {
			h.svc.RecordAuthFailure(r.Context(), ip)
			// PENTEST-005: never differentiate unknown user / wrong password
			// / locked / disabled in the response. All return 401 with the
			// same body so an attacker cannot enumerate valid usernames or
			// account states. Log the actual cause server-side for ops.
			logAuthFailure(r.Context(), err, username, ip)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			return
		}
		userID = user.ID.String()
		userTenantID = user.TenantID.String()
		principalKind = user.Kind
	}

	// Docker /auth/token issues per-action OCI tokens; roles claim is omitted
	// because the OCI client only consumes the `access` scopes. The dashboard's
	// /api/v1/login path goes through Service.Login() which embeds roles.
	// is_global_admin is also false here — OCI clients don't need platform-admin
	// context; they only need the scoped access list.
	// amr is nil here: the Docker /auth/token endpoint serves both password
	// and API-key (Bearer key.) principals, so no single authentication method
	// applies. The dashboard login path (Service.Login) stamps ["pwd"].
	// sid is "" here: OCI Docker tokens are not interactive sessions, so they
	// carry no session id (they are never listed or refreshed).
	tok, err := h.svc.IssueToken(r.Context(), userID, userTenantID, access, nil, false, principalKind, nil, "")
	if err != nil {
		slog.ErrorContext(r.Context(), "issue token failed", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Token:       tok,
		AccessToken: tok,
		ExpiresIn:   300,
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
	})
}

// ── JWKS ─────────────────────────────────────────────────────────────────────

// jwks returns the public key set for JWT verification by other services.
func (h *HTTPHandler) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.JWKS())
}

// ── User management ──────────────────────────────────────────────────────────

// createUser registers a new user account.
//
// PENTEST-003 (2026-06-18): This endpoint used to be unauthenticated and
// accepted any `tenant_id` from the request body. That permitted unauthenticated
// account squatting, cross-tenant user injection, and a username-enumeration
// oracle via the 409 conflict response. The endpoint now:
//  1. Requires a valid Bearer token (requireAuth).
//  2. Uses the caller's tenant_id from the JWT — body.tenant_id is ignored
//     (or, if supplied, must match the caller's tenant).
//  3. Requires the caller to hold an `admin` or `owner` role somewhere in
//     that tenant. New users always start with zero role assignments, so
//     bootstrapping the very first admin must happen through a seed migration
//     or out-of-band tooling, never through this endpoint.
func (h *HTTPHandler) createUser(w http.ResponseWriter, r *http.Request) {
	caller, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req struct {
		TenantID    string `json:"tenant_id"` // optional; must match caller's tenant if supplied
		Username    string `json:"username"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	callerTenantID, err := uuid.Parse(caller.TenantID)
	if err != nil {
		// Malformed claim — fail closed and surface only a generic error.
		slog.ErrorContext(r.Context(), "createUser: caller token has invalid tenant_id", "value", caller.TenantID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// If body.tenant_id is supplied it MUST equal the caller's tenant. We do not
	// accept cross-tenant user creation through this endpoint — a platform-admin
	// flow can be added later as a separate, super-admin-gated route.
	if req.TenantID != "" && req.TenantID != caller.TenantID {
		writeError(w, http.StatusForbidden, "DENIED", "cannot create users in a different tenant")
		return
	}
	tenantID := callerTenantID

	callerUserID, err := uuid.Parse(caller.Subject)
	if err != nil {
		slog.ErrorContext(r.Context(), "createUser: caller token has invalid sub", "value", caller.Subject)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Caller must be admin or owner of at least one scope in the tenant. New
	// users start with zero RBAC assignments, so this is a coarse-but-correct
	// gate ("are you any kind of admin here?") — once the user exists, granting
	// scoped roles is itself scope-gated (PENTEST-002), preventing an admin of
	// org-A from elevating the new user to admin of org-B.
	if !callerIsTenantAdmin(r.Context(), h.svc, callerUserID, tenantID, caller.PrincipalKind) {
		writeError(w, http.StatusForbidden, "DENIED", "admin role required to create users")
		return
	}

	// REM-018: enforce non-empty display_name on the public POST /api/v1/users
	// path. The dashboard's members table, audit-event actor column, and
	// granted-by column all want a human label; allowing it to be empty here
	// would propagate UUID-fallbacks downstream. This gate fires AFTER the
	// security checks above (PENTEST-002 / PENTEST-003) so a non-admin or
	// cross-tenant caller still sees 403, not 400. Length + content
	// validation happens inside service.CreateUser via the shared
	// validateDisplayName helper.
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "display_name is required")
		return
	}

	user, err := h.svc.CreateUser(r.Context(), tenantID, req.Username, req.Email, req.DisplayName, req.Password)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "username or email already in use")
			return
		}
		// ValidatePassword returns plain errors.New("password must ...") messages
		// that are safe to forward to callers so they know which policy was
		// violated. Hash failures from argon2 are wrapped with "hash password: ..."
		// and must never be surfaced — log them and return a generic message.
		if service.IsPasswordPolicyError(err) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
			return
		}
		// REM-018: ErrInvalidDisplayName is safe to surface — the message
		// echoes the same 1..128 char / no control char constraint the
		// PATCH /users/me path already documents.
		if errors.Is(err, service.ErrInvalidDisplayName) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "display_name must be 1..128 characters and contain no control characters")
			return
		}
		slog.ErrorContext(r.Context(), "create user failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "unable to create user")
		return
	}

	// REM-018: surface DisplayName so the FE doesn't need a follow-up GET.
	// User.DisplayName is *string (nullable) so dereference defensively —
	// internal callers may still create users with an empty display_name.
	displayName := ""
	if user.DisplayName != nil {
		displayName = *user.DisplayName
	}
	writeJSON(w, http.StatusCreated, userResponse{
		ID:          user.ID.String(),
		TenantID:    user.TenantID.String(),
		Username:    user.Username,
		Email:       user.Email,
		DisplayName: displayName,
		IsActive:    user.IsActive,
		CreatedAt:   user.CreatedAt,
	})
}

// callerIsTenantAdmin reports whether the user holds an `admin` or `owner` role
// at any scope within the tenant. Used as the gate for tenant-wide privileged
// operations (user creation, service account creation, /me/abilities) where
// there is no narrower target scope to check. Returns false on lookup error —
// fail-closed.
//
// Gate dispatch order (load-bearing; corresponds to PR #194 + #193 stack):
//
//  1. REDESIGN-001 Phase 5.4 / Decision #24 — service-account principals are
//     denied first, before any role lookup. The shadow user behind an SA
//     inherits the human owner's roles, so a naïve role lookup against
//     claims.Subject would let an API key clear admin gates that the SA
//     itself should not be able to clear. Callers must supply the
//     authenticated principal kind (claims.PrincipalKind) so this gate can
//     refuse SA bearers up front.
//
//  2. REDESIGN-001 Phase 5.1 tail (#193 / #197) — users.is_global_admin is a
//     fast-path. The Phase 5.1 backfill deleted the legacy (admin, org, "*")
//     marker without granting an equivalent (admin, tenant, <id>) row, so a
//     brand-new bootstrap admin (is_global_admin=true, no role assignments)
//     was failing every gate that funnels through this helper. The user
//     lookup is fail-open: if GetUserByID errors, fall through to the role
//     lookup so a transient DB blip doesn't lock everyone out.
//
//  3. Standard role lookup — any admin/owner assignment within the tenant
//     clears the gate.
func callerIsTenantAdmin(ctx context.Context, svc *service.Service, userID, tenantID uuid.UUID, principalKind string) bool {
	// Step 1: deny SA principals before any DB read.
	if principalKind == "service_account" {
		return false
	}
	// Step 2: is_global_admin fast-path (fail-open on lookup error).
	if user, err := svc.GetUserByID(ctx, userID); err == nil && user != nil && user.IsGlobalAdmin {
		return true
	}
	// Step 3: scoped role lookup.
	assignments, err := svc.GetUserRoles(ctx, userID, tenantID)
	if err != nil {
		slog.WarnContext(ctx, "callerIsTenantAdmin: GetUserRoles failed", "error", err)
		return false
	}
	for _, a := range assignments {
		if a.RoleName == "admin" || a.RoleName == "owner" {
			return true
		}
	}
	return false
}

// login validates credentials and returns a JWT session token.
func (h *HTTPHandler) login(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if err := h.svc.CheckIPRateLimit(r.Context(), ip); err != nil {
		writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "rate limit exceeded")
		return
	}

	var req struct {
		TenantID string `json:"tenant_id"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid tenant_id")
		return
	}

	res, err := h.svc.Login(r.Context(), tenantID, req.Username, req.Password)
	if err != nil {
		h.svc.RecordAuthFailure(r.Context(), ip)
		// PENTEST-005: collapse all auth failure variants into one 401 response.
		logAuthFailure(r.Context(), err, req.Username, ip)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}

	// Two-step login: branch on the LoginResult. An MFA-enabled user gets a
	// challenge token (spent at POST /login/mfa); a policy-forced un-enrolled
	// user gets a setup token; otherwise the full access token is returned under
	// the same {"token": ...} shape the single-step flow always used.
	switch {
	case res.MFARequired:
		writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true, "challenge_token": res.ChallengeToken})
	case res.MFASetupRequired:
		writeJSON(w, http.StatusOK, map[string]any{"mfa_setup_required": true, "setup_token": res.SetupToken})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"token": res.Token})
	}
}

// logAuthFailure emits a structured server-side log entry classifying an
// authentication failure. Distinguishing lockout / disabled / invalid is fine
// in logs (ops needs to see it) but MUST NOT be leaked back to the caller —
// the HTTP response is always 401 "invalid credentials" so an attacker cannot
// enumerate accounts or account states.
func logAuthFailure(ctx context.Context, err error, username, ip string) {
	switch {
	case errors.Is(err, service.ErrAccountLocked):
		slog.InfoContext(ctx, "auth: rejected login for locked account", "username", username, "ip", ip)
	case errors.Is(err, service.ErrAccountDisabled):
		slog.InfoContext(ctx, "auth: rejected login for disabled account", "username", username, "ip", ip)
	case errors.Is(err, service.ErrInvalidCredentials):
		slog.InfoContext(ctx, "auth: rejected invalid credentials", "username", username, "ip", ip)
	default:
		slog.WarnContext(ctx, "auth: rejected login (unexpected error)", "username", username, "ip", ip, "error", err)
	}
}

// logout revokes the caller's current JWT by JTI.
func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	if err := h.svc.RevokeToken(r.Context(), claims); err != nil {
		slog.ErrorContext(r.Context(), "revoke token failed", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// refreshToken issues a new JWT in exchange for a currently-valid one.
//
// The caller must present its current Bearer token in the Authorization header.
// The token must be valid and non-expired — refresh of an already-expired
// session is not supported and returns 401. On success the old token's JTI is
// revoked in Redis and a fresh token (same subject/tenant, new JTI, new exp)
// is returned. This allows the frontend to silently renew the session without
// prompting the user for credentials again.
func (h *HTTPHandler) refreshToken(w http.ResponseWriter, r *http.Request) {
	// PENTEST-013: scheme name is case-insensitive per RFC 7235.
	rawToken, ok := bearer.Extract(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
		return
	}

	newToken, err := h.svc.RefreshToken(r.Context(), rawToken)
	if err != nil {
		// All errors from RefreshToken (expired, invalid, revoked) map to 401 so
		// callers cannot distinguish them (SEC-036: avoid error oracle attacks).
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": newToken})
}

// ── API key management ────────────────────────────────────────────────────────

// createAPIKeyBody is the request body for POST /api/v1/apikeys.
// ServiceAccountID is optional; when set the request is routed to the
// SA-key issuance path (admin-gated) instead of the human-user path.
type createAPIKeyBody struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// ServiceAccountID, when non-empty, must be a valid UUID. The caller must
	// hold an admin role in the SA's tenant. The key will be owned by the SA
	// (ServiceAccountID column set, UserID nil) instead of the calling user.
	ServiceAccountID *string `json:"service_account_id,omitempty"`
}

// createAPIKey generates a new API key and returns the raw secret (shown once only).
//
// When the request body contains a non-empty service_account_id the call is
// forwarded to createSAAPIKey which handles admin gating and SA-key issuance.
// The human-key path is unchanged when service_account_id is absent or empty.
func (h *HTTPHandler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req createAPIKeyBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	// Branch: when service_account_id is supplied, route to SA-key issuance.
	if req.ServiceAccountID != nil && *req.ServiceAccountID != "" {
		h.createSAAPIKey(w, r, req, claims)
		return
	}

	// Human-user key path (unchanged).
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "invalid tenant in token")
		return
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "invalid user in token")
		return
	}

	key, rawSecret, err := h.svc.CreateAPIKey(r.Context(), tenantID, userID, req.Name, req.Scopes, req.ExpiresAt)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "api key with this name already exists")
			return
		}
		// Log the actual error so a future regression doesn't recur in
		// silence. The response stays generic per the never-leak-internals
		// rule (CLAUDE.md §13), but the operator gets the real cause in
		// the structured log.
		slog.ErrorContext(r.Context(), "createAPIKey: service error",
			"err", err,
			"tenant_id", tenantID.String(),
			"user_id", userID.String(),
			"name", req.Name,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, apiKeyResponse{
		ID:        key.ID.String(),
		Name:      key.Name,
		Prefix:    key.KeyPrefix,
		Scopes:    key.Scopes,
		ExpiresAt: key.ExpiresAt,
		CreatedAt: key.CreatedAt,
		RawKey:    rawSecret,
	})
}

// createSAAPIKey handles the SA-key issuance branch of POST /api/v1/apikeys.
//
// It is called only when the decoded request body contains a non-empty
// service_account_id. The flow mirrors saIssueKey in http_service_accounts.go
// but is reached from the /apikeys endpoint rather than the SA-specific URL:
//
//  1. Validate service_account_id is a well-formed UUID.
//  2. Require saService to be configured (501 if not).
//  3. Load the SA and enforce tenant ownership (404 on mismatch).
//  4. Require the caller to be a workspace admin (403 if not).
//  5. Delegate to saService.IssueKey and return 201 with the same
//     apiKeyResponse shape as the human-key path.
func (h *HTTPHandler) createSAAPIKey(w http.ResponseWriter, r *http.Request, req createAPIKeyBody, claims *service.Claims) {
	// saService must be wired; return 501 when it is not.
	if !h.requireSAService(w) {
		return
	}

	// 1. Parse and validate the SA UUID.
	saID, err := uuid.Parse(*req.ServiceAccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid service_account_id")
		return
	}

	callerID, err := uuid.Parse(claims.Subject)
	if err != nil {
		slog.ErrorContext(r.Context(), "createSAAPIKey: invalid sub in token", "value", claims.Subject)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	callerTenant, err := uuid.Parse(claims.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "createSAAPIKey: invalid tenant_id in token", "value", claims.TenantID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// 2. Load the SA; verify it belongs to the caller's tenant. We return 404
	// rather than 403 on a tenant mismatch so callers cannot probe cross-tenant
	// SA existence via this endpoint.
	sa, err := h.saService.Get(r.Context(), saID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "service account not found")
			return
		}
		slog.ErrorContext(r.Context(), "createSAAPIKey: Get failed",
			"sa_id", saID,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if sa.TenantID != callerTenant {
		// Surface cross-tenant mismatch as 404 — same pattern as getServiceAccount
		// and deleteServiceAccount (CLAUDE.md §9: never expose cross-tenant existence).
		writeError(w, http.StatusNotFound, "NOT_FOUND", "service account not found")
		return
	}

	// 3. Require the caller to be a tenant admin (admin or owner role).
	if !callerIsTenantAdmin(r.Context(), h.svc, callerID, callerTenant, claims.PrincipalKind) {
		writeError(w, http.StatusForbidden, "DENIED", "admin role required")
		return
	}

	// 4. Issue the key via the service layer; it validates that scopes are a
	// subset of sa.AllowedScopes and emits the service_account.key_issued audit
	// event.
	result, err := h.saService.IssueKey(r.Context(), sa.ID, sa.TenantID, req.Name, req.Scopes, callerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "service account not found")
			return
		}
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "a key with that name already exists for this service account")
			return
		}
		// Check for scope-not-allowed; return 400 with the denied scope name so
		// callers receive a precise, actionable error message.
		var scopeErr *service.ErrScopeNotAllowed
		if errors.As(err, &scopeErr) {
			writeError(w, http.StatusBadRequest, "SCOPE_NOT_ALLOWED",
				"scope not allowed for this service account: "+scopeErr.Scope)
			return
		}
		slog.ErrorContext(r.Context(), "createSAAPIKey: IssueKey failed",
			"sa_id", sa.ID,
			"tenant_id", sa.TenantID,
			"name", req.Name,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// 5. Return 201 with the same shape as the human-key response (apiKeyResponse).
	// RawKey is the plaintext secret — shown exactly once, never recoverable.
	k := result.Key
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	writeJSON(w, http.StatusCreated, apiKeyResponse{
		ID:        k.ID.String(),
		Name:      k.Name,
		Prefix:    k.KeyPrefix,
		Scopes:    scopes,
		ExpiresAt: k.ExpiresAt,
		CreatedAt: k.CreatedAt,
		RawKey:    result.RawSecret,
	})
}

// listAPIKeys returns all active API keys owned by the authenticated user.
func (h *HTTPHandler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "invalid user in token")
		return
	}

	keys, err := h.svc.ListAPIKeys(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	resp := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		resp[i] = apiKeyResponse{
			ID:         k.ID.String(),
			Name:       k.Name,
			Prefix:     k.KeyPrefix,
			Scopes:     k.Scopes,
			ExpiresAt:  k.ExpiresAt,
			CreatedAt:  k.CreatedAt,
			LastUsedAt: k.LastUsedAt,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// deleteAPIKey soft-deletes the API key identified by the {id} path segment.
func (h *HTTPHandler) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	keyID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid key id")
		return
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "invalid user in token")
		return
	}

	if err := h.svc.DeleteAPIKey(r.Context(), keyID, userID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "api key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// requireAuth extracts and validates the Bearer token from the request.
// PENTEST-013: uses the case-insensitive bearer.Extract helper.
//
// FUT-006 (2026-06-23): accepts API keys in addition to JWTs. The
// discriminator is the literal `key.` prefix — a token of shape
// `key.<uuid>.<64-hex-secret>` is dispatched to ValidateAPIKey; anything
// else is treated as a JWT. This unifies the auth model so a CI bot can
// introspect itself via `GET /users/me` with `Authorization: Bearer
// key.<id>.<secret>` instead of having to do the `/auth/token` JWT
// exchange dance first.
//
// On API-key success we synthesise a `*service.Claims` whose Subject is
// the SA's shadow user id (or the human user id for human-owned keys),
// TenantID is the key's tenant, and Access carries the EffectiveScopes
// already-intersected against the SA allowlist. Roles are intentionally
// empty — raw API keys don't carry RBAC roles (those are resolved at
// JWT issuance time); downstream handlers that need roles must still go
// through the JWT exchange.
func (h *HTTPHandler) requireAuth(r *http.Request) (*service.Claims, error) {
	token, ok := bearer.Extract(r.Header.Get("Authorization"))
	if !ok {
		return nil, errors.New("missing bearer token")
	}
	if keyID, secret, ok := parseAPIKeyBearer(token); ok {
		vk, err := h.svc.ValidateAPIKey(r.Context(), service.ValidateAPIKeyOpts{
			KeyID:     keyID,
			RawSecret: secret,
			// RequestTenantID intentionally left nil — the gateway-injected
			// X-Tenant-ID header isn't reliably present on every /users/me
			// call (the FE may not set it for bearer-key callers). The
			// cross-tenant guard still fires on the gRPC ValidateAPIKey path
			// for OCI pulls; here we accept the SA's stored tenant.
		})
		if err != nil {
			return nil, err
		}
		return synthClaimsFromAPIKey(vk), nil
	}
	return h.svc.ValidateToken(r.Context(), token)
}

// parseAPIKeyBearer tries to interpret `token` as an API-key Bearer of the
// form `key.<uuid>.<secret>`. Returns (keyID, secret, true) on success.
// On any structural mismatch (missing prefix, wrong number of segments,
// unparseable UUID, empty secret) returns the zero value + false so the
// caller falls through to JWT validation. We never log or surface the
// secret here — leaking a partial match would be worse than a generic
// auth failure downstream.
func parseAPIKeyBearer(token string) (uuid.UUID, string, bool) {
	const prefix = "key."
	if !strings.HasPrefix(token, prefix) {
		return uuid.Nil, "", false
	}
	rest := token[len(prefix):]
	// Split on the next dot only — the secret itself is 64 lowercase hex
	// chars, no dots, so a SplitN(2) is exact.
	idStr, secret, ok := strings.Cut(rest, ".")
	if !ok || secret == "" {
		return uuid.Nil, "", false
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, "", false
	}
	return id, secret, true
}

// synthClaimsFromAPIKey builds a *service.Claims that mirrors what a JWT
// would carry for the same principal. Subject is the user_id (the
// shadow user for SA-owned keys per FE-API-048 T6) so downstream
// handlers branch on user.Kind exactly as they do for JWT callers.
//
// FUT-006: Roles is left empty because raw API keys don't have a roles
// claim baked in. Handlers that gate on a specific role (admin / owner)
// must still require a JWT — they will surface a clean 403 against the
// empty roles list rather than a confusing UNAUTHORIZED.
func synthClaimsFromAPIKey(vk *service.ValidatedKey) *service.Claims {
	return &service.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: vk.UserID.String(),
		},
		TenantID: vk.TenantID.String(),
		Access:   vk.Access,
		// PrincipalKind is propagated so admin gates (callerIsTenantAdmin and
		// the management require*Admin helpers) can deny service-account
		// principals regardless of the owner's role assignments
		// (REDESIGN-001 Phase 5.4, Decision #24).
		PrincipalKind: vk.PrincipalKind,
	}
}

// parseTenantID reads the X-Tenant-ID header injected by the gateway.
// Falls back to h.devDefaultTenant when the header is absent and a dev default is configured.
func (h *HTTPHandler) parseTenantID(r *http.Request) (uuid.UUID, error) {
	raw := r.Header.Get("X-Tenant-ID")
	if raw == "" {
		if h.devDefaultTenant != uuid.Nil {
			return h.devDefaultTenant, nil
		}
		return uuid.Nil, errors.New("missing X-Tenant-ID header")
	}
	return uuid.Parse(raw)
}

// parseScopes parses Docker auth scope strings (e.g. "repository:org/repo:pull,push").
// Multiple scopes may appear as repeated query params or space-separated within one param.
func parseScopes(scopeParams []string) []service.RepositoryAccess {
	var access []service.RepositoryAccess
	for _, param := range scopeParams {
		for _, s := range strings.Fields(param) {
			parts := strings.SplitN(s, ":", 3)
			if len(parts) != 3 {
				continue
			}
			var actions []string
			for _, a := range strings.Split(parts[2], ",") {
				if a != "" {
					actions = append(actions, a)
				}
			}
			if len(actions) > 0 {
				access = append(access, service.RepositoryAccess{
					Type:    parts[0],
					Name:    parts[1],
					Actions: actions,
				})
			}
		}
	}
	return access
}

// remoteIP returns the client IP for rate limiting purposes.
//
// When the TCP peer address belongs to a configured trusted proxy
// (TRUSTED_PROXY_CIDRS), the leftmost non-private, non-loopback address in
// X-Forwarded-For is returned instead. This ensures that per-client limits are
// enforced against the actual originating IP rather than the proxy's address.
//
// If XFF is absent, empty, or contains only private/loopback addresses the
// function falls back to the TCP peer address so rate limiting is never
// silently disabled.
func remoteIP(r *http.Request) string {
	// Extract the raw TCP peer IP; SplitHostPort strips the port number.
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr has no port (unusual but possible in tests); use as-is.
		peer = r.RemoteAddr
	}

	peerIP := net.ParseIP(peer)
	if peerIP != nil && isTrustedProxy(peerIP) {
		// The request came through a trusted proxy — honour X-Forwarded-For.
		// Use the leftmost address that is not private or loopback; that is the
		// original client IP that the proxy prepended to the header chain.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			for _, part := range strings.Split(xff, ",") {
				ip := net.ParseIP(strings.TrimSpace(part))
				if ip != nil && !ip.IsPrivate() && !ip.IsLoopback() {
					return ip.String()
				}
			}
		}
	}

	// No trusted proxy, or XFF yielded no usable public IP — use TCP peer.
	return peer
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write JSON response", "err", err)
	}
}

func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	writeJSON(w, statusCode, map[string]any{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}

// ── Response types ────────────────────────────────────────────────────────────

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

type userResponse struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

type apiKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// LastUsedAt is populated on list responses (and is nil for keys that
	// have never been used). On creation responses it is always nil since
	// the key has not yet been validated against any request. Sprint 11
	// maint batch 1 (B2): the column has existed in api_keys since
	// FE-API-048 T6 + is touched by ValidateAPIKey via repository.TouchLastUsed,
	// but the JSON response was never plumbed through — the frontend
	// table's "Last used" column always rendered "Never" as a result.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// RawKey is only populated on creation; empty on list responses.
	RawKey string `json:"key,omitempty"`
}
