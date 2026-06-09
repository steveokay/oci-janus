package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// HTTPHandler implements the HTTP API for registry-auth.
type HTTPHandler struct {
	svc *service.Service
}

// NewHTTPHandler creates an HTTPHandler backed by the given service.
func NewHTTPHandler(svc *service.Service) *HTTPHandler {
	return &HTTPHandler{svc: svc}
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

	tenantID, err := parseTenantID(r)
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
		// All other errors from CreateUser are either password validation failures
		// (bare errors.New) or hash failures (extremely rare internal errors).
		writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
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
func parseTenantID(r *http.Request) (uuid.UUID, error) {
	raw := r.Header.Get("X-Tenant-ID")
	if raw == "" {
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

// remoteIP extracts the client IP address from r.RemoteAddr.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
