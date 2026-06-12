package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

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

// Register mounts all auth routes onto mux.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	// Docker token auth — RFC 7235 flow; Docker clients use GET, some tools use POST.
	mux.HandleFunc("POST /auth/token", h.token)
	mux.HandleFunc("GET /auth/token", h.token)
	mux.HandleFunc("GET /.well-known/jwks.json", h.jwks)
	mux.HandleFunc("POST /api/v1/users", h.createUser)
	mux.HandleFunc("POST /api/v1/login", h.login)
	mux.HandleFunc("POST /api/v1/logout", h.logout)
	mux.HandleFunc("POST /api/v1/apikeys", h.createAPIKey)
	mux.HandleFunc("GET /api/v1/apikeys", h.listAPIKeys)
	mux.HandleFunc("DELETE /api/v1/apikeys/{id}", h.deleteAPIKey)
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
		key, err := h.svc.ValidateAPIKey(r.Context(), keyID, password)
		if err != nil {
			h.svc.RecordAuthFailure(r.Context(), ip)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			return
		}
		userID = key.UserID.String()
		userTenantID = key.TenantID.String()
	} else {
		user, err := h.svc.AuthenticateUser(r.Context(), tenantID, username, password)
		if err != nil {
			h.svc.RecordAuthFailure(r.Context(), ip)
			switch {
			case errors.Is(err, service.ErrAccountLocked):
				writeError(w, http.StatusForbidden, "DENIED", "account locked")
			case errors.Is(err, service.ErrAccountDisabled):
				writeError(w, http.StatusForbidden, "DENIED", "account disabled")
			default:
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			}
			return
		}
		userID = user.ID.String()
		userTenantID = user.TenantID.String()
	}

	tok, err := h.svc.IssueToken(r.Context(), userID, userTenantID, access)
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
func (h *HTTPHandler) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID string `json:"tenant_id"`
		Username string `json:"username"`
		Email    string `json:"email"`
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
		switch {
		case errors.Is(err, service.ErrAccountLocked):
			writeError(w, http.StatusForbidden, "FORBIDDEN", "account locked")
		case errors.Is(err, service.ErrAccountDisabled):
			writeError(w, http.StatusForbidden, "FORBIDDEN", "account disabled")
		default:
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
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
func (h *HTTPHandler) requireAuth(r *http.Request) (*service.Claims, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, errors.New("missing bearer token")
	}
	return h.svc.ValidateToken(r.Context(), strings.TrimPrefix(auth, "Bearer "))
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
