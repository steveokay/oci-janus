package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// SSOEventPublisher is the narrow contract the SSO admin handlers need from
// the RabbitMQ publisher. Lets tests substitute a no-op without standing up
// a broker; *publisher.Publisher satisfies the interface.
type SSOEventPublisher interface {
	Publish(ctx context.Context, routingKey string, event events.Event) error
}

// trustedProxyCIDRs holds the parsed CIDR ranges that are allowed to set
// X-Forwarded-For. Populated from TRUSTED_PROXY_CIDRS at process startup.
// An empty slice means no proxy is trusted and RemoteAddr is always used.
var trustedProxyCIDRs []*net.IPNet

// init parses TRUSTED_PROXY_CIDRS (comma-separated CIDR notation) once at
// startup. Invalid CIDR entries are silently skipped so a misconfigured value
// degrades gracefully (falls back to RemoteAddr) rather than panicking.
func init() {
	raw := os.Getenv("TRUSTED_PROXY_CIDRS")
	if raw == "" {
		return
	}
	for _, cidr := range strings.Split(raw, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			trustedProxyCIDRs = append(trustedProxyCIDRs, network)
		} else {
			// A malformed CIDR is skipped so startup is not blocked, but the operator
			// should know their TRUSTED_PROXY_CIDRS value has an invalid entry because
			// the remaining valid entries may not provide the expected coverage.
			slog.Warn("TRUSTED_PROXY_CIDRS: invalid CIDR skipped", "entry", cidr, "error", err)
		}
	}
}

// isTrustedProxy reports whether ip falls within one of the configured trusted
// proxy CIDRs. Returns false when trustedProxyCIDRs is empty (no proxy trust).
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
	// eventPublisher is the optional RabbitMQ publisher used by the SSO admin
	// CRUD audit events. nil silently skips the publish.
	eventPublisher SSOEventPublisher
	// samlConfig is the optional SAML SP signing keypair (FE-API-034). nil
	// disables SAML support — the /auth/saml/... routes return 501.
	samlConfig *saml.SPConfig
	// saService is the optional ServiceAccountService (FE-API-048, T13). nil
	// causes all /api/v1/service-accounts routes to return 501 NOT_IMPLEMENTED.
	// Set via WithServiceAccountService.
	saService *service.ServiceAccountService
	// activityService is the optional ActivityService (FE-API-048, T15). nil
	// causes GET /api/v1/access/activity to return 501 NOT_IMPLEMENTED.
	// Set via WithActivityService.
	activityService *service.ActivityService
}

// NewHTTPHandler creates an HTTPHandler backed by the given service.
// devDefaultTenantID may be uuid.Nil (production) or a fixed dev UUID.
//
// A warning is logged when TRUSTED_PROXY_CIDRS is not set because IP-based
// rate limiting will then target the TCP peer (the proxy) rather than the
// actual client, rendering per-client rate limits ineffective in deployed
// environments where traffic arrives through a load balancer or ingress proxy.
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

// WithEventPublisher wires the RabbitMQ publisher used by the SSO admin
// CRUD audit events. Optional; nil publisher silently skips publishes.
func (h *HTTPHandler) WithEventPublisher(p SSOEventPublisher) *HTTPHandler {
	h.eventPublisher = p
	return h
}

// WithSAMLConfig attaches the SP signing keypair used by the SAML routes.
// Optional — without it the /auth/saml/... routes return 501. Returns the
// handler so callers can chain.
func (h *HTTPHandler) WithSAMLConfig(cfg *saml.SPConfig) *HTTPHandler {
	h.samlConfig = cfg
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
	mux.HandleFunc("POST /api/v1/users", h.createUser)
	mux.HandleFunc("POST /api/v1/login", h.login)
	mux.HandleFunc("POST /api/v1/logout", h.logout)
	mux.HandleFunc("POST /api/v1/token/refresh", h.refreshToken)
	mux.HandleFunc("POST /api/v1/apikeys", h.createAPIKey)
	mux.HandleFunc("GET /api/v1/apikeys", h.listAPIKeys)
	mux.HandleFunc("DELETE /api/v1/apikeys/{id}", h.deleteAPIKey)
	// FE-API-011 / FE-API-012 / FE-API-013 — current-user profile & password.
	mux.HandleFunc("GET /api/v1/users/me", h.getCurrentUser)
	mux.HandleFunc("PATCH /api/v1/users/me", h.updateCurrentUser)
	mux.HandleFunc("POST /api/v1/users/me/password", h.changeCurrentUserPassword)
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

	var userID, userTenantID string

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
	}

	// Docker /auth/token issues per-action OCI tokens; roles claim is omitted
	// because the OCI client only consumes the `access` scopes. The dashboard's
	// /api/v1/login path goes through Service.Login() which embeds roles.
	tok, err := h.svc.IssueToken(r.Context(), userID, userTenantID, access, nil)
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
//   1. Requires a valid Bearer token (requireAuth).
//   2. Uses the caller's tenant_id from the JWT — body.tenant_id is ignored
//      (or, if supplied, must match the caller's tenant).
//   3. Requires the caller to hold an `admin` or `owner` role somewhere in
//      that tenant. New users always start with zero role assignments, so
//      bootstrapping the very first admin must happen through a seed migration
//      or out-of-band tooling, never through this endpoint.
func (h *HTTPHandler) createUser(w http.ResponseWriter, r *http.Request) {
	caller, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req struct {
		TenantID string `json:"tenant_id"` // optional; must match caller's tenant if supplied
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
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
	if !callerIsTenantAdmin(r.Context(), h.svc, callerUserID, tenantID) {
		writeError(w, http.StatusForbidden, "DENIED", "admin role required to create users")
		return
	}

	user, err := h.svc.CreateUser(r.Context(), tenantID, req.Username, req.Email, req.Password)
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
		slog.ErrorContext(r.Context(), "create user failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "unable to create user")
		return
	}

	writeJSON(w, http.StatusCreated, userResponse{
		ID:        user.ID.String(),
		TenantID:  user.TenantID.String(),
		Username:  user.Username,
		Email:     user.Email,
		IsActive:  user.IsActive,
		CreatedAt: user.CreatedAt,
	})
}

// callerIsTenantAdmin reports whether the user holds an `admin` or `owner` role
// at any scope within the tenant. Used as the gate for tenant-wide privileged
// operations (currently: user creation) where there is no narrower target scope
// to check. Returns false on lookup error — fail-closed.
func callerIsTenantAdmin(ctx context.Context, svc *service.Service, userID, tenantID uuid.UUID) bool {
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

	tok, err := h.svc.Login(r.Context(), tenantID, req.Username, req.Password)
	if err != nil {
		h.svc.RecordAuthFailure(r.Context(), ip)
		// PENTEST-005: collapse all auth failure variants into one 401 response.
		logAuthFailure(r.Context(), err, req.Username, ip)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
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

// createAPIKey generates a new API key and returns the raw secret (shown once only).
func (h *HTTPHandler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

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
			ID:        k.ID.String(),
			Name:      k.Name,
			Prefix:    k.KeyPrefix,
			Scopes:    k.Scopes,
			ExpiresAt: k.ExpiresAt,
			CreatedAt: k.CreatedAt,
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
func (h *HTTPHandler) requireAuth(r *http.Request) (*service.Claims, error) {
	token, ok := bearer.Extract(r.Header.Get("Authorization"))
	if !ok {
		return nil, errors.New("missing bearer token")
	}
	return h.svc.ValidateToken(r.Context(), token)
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
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

type apiKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// RawKey is only populated on creation; empty on list responses.
	RawKey string `json:"key,omitempty"`
}
