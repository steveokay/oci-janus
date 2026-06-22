// http_service_accounts.go — HTTP handler for /api/v1/service-accounts (FE-API-048, Task 13).
//
// All five CRUD endpoints are implemented here: list, create, get, update, delete.
// Four additional routes are registered as 501 stubs (scopesPreflight, listKeys,
// issueKey, revokeKey) — T14 fills those in.
//
// Admin gate: every route requires that the caller holds an "admin" or "owner"
// role in their own tenant (same gate used by createUser and adminCreateAuthProvider).
// Cross-tenant access is not supported for SA management; the caller's tenant_id
// from the JWT is used as the authoritative tenant for all operations.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// Input validation regexes (CLAUDE.md §7).
// saNameRe matches the service-account name allowlist: lowercase alphanumeric
// with optional single separators (., -, _), max 64 characters.
var (
	saNameRe  = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
	saScpRe   = regexp.MustCompile(`^[a-z][a-z0-9_:]{0,63}$`)
)

// saMaxNameLen is the maximum length for a service-account name (CLAUDE.md §7).
const (
	saMaxNameLen  = 64
	saMaxDescLen  = 280
)

// serviceAccountResponse is the JSON shape returned by all SA CRUD endpoints.
// Nullable fields (created_by, disabled_at) use omitempty so they are absent
// rather than null in the JSON output, matching the spec §5 shape.
type serviceAccountResponse struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	AllowedScopes  []string   `json:"allowed_scopes"`
	ShadowUserID   string     `json:"shadow_user_id"`
	// CreatedBy is the UUID of the human who created the SA, or empty when
	// the creator's account has been deleted (ON DELETE SET NULL).
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	// DisabledAt is absent while the account is active.
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
	// ActiveKeyCount carries the live key count from ServiceAccountWithStats.
	// It is 0 on create/get/update responses (no stats join on single-row
	// fetches); T14 enriches the list path.
	ActiveKeyCount int32      `json:"active_key_count"`
}

// saToResponse converts a repository.ServiceAccount to the wire response.
func saToResponse(sa *repository.ServiceAccount) serviceAccountResponse {
	resp := serviceAccountResponse{
		ID:            sa.ID.String(),
		TenantID:      sa.TenantID.String(),
		Name:          sa.Name,
		Description:   sa.Description,
		AllowedScopes: sa.AllowedScopes,
		ShadowUserID:  sa.ShadowUserID.String(),
		CreatedAt:     sa.CreatedAt,
		DisabledAt:    sa.DisabledAt,
	}
	if sa.CreatedBy != nil {
		resp.CreatedBy = sa.CreatedBy.String()
	}
	// Ensure AllowedScopes is never serialised as JSON null.
	if resp.AllowedScopes == nil {
		resp.AllowedScopes = []string{}
	}
	return resp
}

// saWithStatsToResponse converts a ServiceAccountWithStats to the wire response,
// preserving the ActiveKeyCount and LastUsedAt from the stats join.
func saWithStatsToResponse(s repository.ServiceAccountWithStats) serviceAccountResponse {
	resp := saToResponse(&s.ServiceAccount)
	resp.ActiveKeyCount = s.ActiveKeyCount
	return resp
}

// RegisterServiceAccounts mounts the service-account routes onto mux. Called
// from HTTPHandler.Register. When sa is nil the routes return 501 so the
// handler degrades gracefully in test builds that do not wire a SA service.
func (h *HTTPHandler) RegisterServiceAccounts(mux *http.ServeMux) {
	// CRUD
	mux.HandleFunc("GET /api/v1/service-accounts", h.listServiceAccounts)
	mux.HandleFunc("POST /api/v1/service-accounts", h.createServiceAccount)
	mux.HandleFunc("GET /api/v1/service-accounts/{id}", h.getServiceAccount)
	mux.HandleFunc("PATCH /api/v1/service-accounts/{id}", h.updateServiceAccount)
	mux.HandleFunc("DELETE /api/v1/service-accounts/{id}", h.deleteServiceAccount)

	// T14 stubs — registered so the URL space is reserved; bodies return 501.
	mux.HandleFunc("POST /api/v1/service-accounts/{id}/scopes/preflight", h.saPreflightScopes)
	mux.HandleFunc("GET /api/v1/service-accounts/{id}/api-keys", h.saListKeys)
	mux.HandleFunc("POST /api/v1/service-accounts/{id}/api-keys", h.saIssueKey)
	mux.HandleFunc("DELETE /api/v1/service-accounts/{id}/api-keys/{keyID}", h.saRevokeKey)
}

// ── authorization helper ──────────────────────────────────────────────────────

// requireSAAdmin extracts the caller's claims and asserts that they hold the
// "admin" or "owner" role in their tenant. Returns (callerUserID, tenantID, nil)
// on success or writes the appropriate error response and returns a non-nil sentinel.
//
// The sentinel error is only used to signal "the response was already written" —
// callers must return immediately when err != nil.
func (h *HTTPHandler) requireSAAdmin(w http.ResponseWriter, r *http.Request) (callerID, tenantID uuid.UUID, err error) {
	// Authenticate the caller.
	claims, authErr := h.requireAuth(r)
	if authErr != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return uuid.Nil, uuid.Nil, authErr
	}

	callerID, parseErr := uuid.Parse(claims.Subject)
	if parseErr != nil {
		slog.ErrorContext(r.Context(), "sa handler: invalid sub in token", "value", claims.Subject)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return uuid.Nil, uuid.Nil, parseErr
	}

	tenantID, parseErr = uuid.Parse(claims.TenantID)
	if parseErr != nil {
		slog.ErrorContext(r.Context(), "sa handler: invalid tenant_id in token", "value", claims.TenantID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return uuid.Nil, uuid.Nil, parseErr
	}

	// Require admin or owner role in the caller's tenant.
	if !callerIsTenantAdmin(r.Context(), h.svc, callerID, tenantID) {
		writeError(w, http.StatusForbidden, "DENIED", "admin role required")
		return uuid.Nil, uuid.Nil, errors.New("forbidden")
	}

	return callerID, tenantID, nil
}

// requireSAService writes 501 and returns false when h.saService is nil.
// Callers must return immediately when it returns false.
func (h *HTTPHandler) requireSAService(w http.ResponseWriter) bool {
	if h.saService == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "service accounts not configured")
		return false
	}
	return true
}

// ── LIST /api/v1/service-accounts ────────────────────────────────────────────

// listServiceAccounts handles GET /api/v1/service-accounts.
//
// Optional query params:
//   - include_disabled=true — include disabled accounts (default: active only)
//   - page_size=N           — max results per page (default: 50, max: 200)
//   - page_token=<tok>      — cursor for the next page
func (h *HTTPHandler) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	// Gate: admin only + SA service must be configured.
	if !h.requireSAService(w) {
		return
	}
	_, tenantID, err := h.requireSAAdmin(w, r)
	if err != nil {
		return
	}

	// Parse query parameters.
	includeDisabled := r.URL.Query().Get("include_disabled") == "true"
	pageToken := r.URL.Query().Get("page_token")
	pageSize := 50
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		n := 0
		for _, ch := range raw {
			if ch < '0' || ch > '9' {
				writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid page_size")
				return
			}
			n = n*10 + int(ch-'0')
		}
		if n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "page_size must be between 1 and 200")
			return
		}
		pageSize = n
	}

	accounts, nextToken, err := h.saService.List(r.Context(), tenantID, includeDisabled, pageSize, pageToken)
	if err != nil {
		slog.ErrorContext(r.Context(), "sa handler: List failed",
			"tenant_id", tenantID,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	items := make([]serviceAccountResponse, len(accounts))
	for i, a := range accounts {
		items[i] = saWithStatsToResponse(a)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service_accounts": items,
		"next_page_token":  nextToken,
	})
}

// ── POST /api/v1/service-accounts ────────────────────────────────────────────

// createServiceAccountBody is the request body for POST /api/v1/service-accounts.
type createServiceAccountBody struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	AllowedScopes []string `json:"allowed_scopes"`
}

// createServiceAccount handles POST /api/v1/service-accounts.
func (h *HTTPHandler) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireSAService(w) {
		return
	}
	callerID, tenantID, err := h.requireSAAdmin(w, r)
	if err != nil {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<18) // 256 KiB
	var body createServiceAccountBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	// Validate name.
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "name is required")
		return
	}
	if len(body.Name) > saMaxNameLen {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "name exceeds maximum length of 64 characters")
		return
	}
	if !saNameRe.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "name must match ^[a-z0-9]+([._-][a-z0-9]+)*$")
		return
	}

	// Validate description.
	if len(body.Description) > saMaxDescLen {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "description exceeds maximum length of 280 characters")
		return
	}

	// Validate allowed_scopes.
	if err := validateScopes(body.AllowedScopes); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
		return
	}

	sa, err := h.saService.Create(r.Context(), service.ServiceAccountInput{
		TenantID:      tenantID,
		Name:          body.Name,
		Description:   body.Description,
		AllowedScopes: body.AllowedScopes,
		ActorUserID:   callerID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "a service account with that name already exists")
			return
		}
		slog.ErrorContext(r.Context(), "sa handler: Create failed",
			"tenant_id", tenantID,
			"name", body.Name,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, saToResponse(sa))
}

// ── GET /api/v1/service-accounts/{id} ────────────────────────────────────────

// getServiceAccount handles GET /api/v1/service-accounts/{id}.
func (h *HTTPHandler) getServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireSAService(w) {
		return
	}
	_, tenantID, err := h.requireSAAdmin(w, r)
	if err != nil {
		return
	}

	saID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid service account id")
		return
	}

	sa, err := h.saService.Get(r.Context(), saID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
			return
		}
		slog.ErrorContext(r.Context(), "sa handler: Get failed", "id", saID, "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Tenant isolation: caller may only see their own tenant's SAs.
	if sa.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
		return
	}

	writeJSON(w, http.StatusOK, saToResponse(sa))
}

// ── PATCH /api/v1/service-accounts/{id} ──────────────────────────────────────

// updateServiceAccountBody is the request body for PATCH /api/v1/service-accounts/{id}.
// All fields are optional; absent fields leave the stored value unchanged (pointer = nil).
type updateServiceAccountBody struct {
	Name          *string   `json:"name,omitempty"`
	Description   *string   `json:"description,omitempty"`
	AllowedScopes *[]string `json:"allowed_scopes,omitempty"`
	// Disabled maps to SetDisabled when present. Using a pointer so that
	// "disabled": false explicitly re-enables rather than being a no-op.
	Disabled *bool `json:"disabled,omitempty"`
}

// updateServiceAccount handles PATCH /api/v1/service-accounts/{id}.
func (h *HTTPHandler) updateServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireSAService(w) {
		return
	}
	callerID, tenantID, err := h.requireSAAdmin(w, r)
	if err != nil {
		return
	}

	saID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid service account id")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<18) // 256 KiB
	var body updateServiceAccountBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	// Validate optional name when provided.
	if body.Name != nil {
		if *body.Name == "" {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "name must not be empty")
			return
		}
		if len(*body.Name) > saMaxNameLen {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "name exceeds maximum length of 64 characters")
			return
		}
		if !saNameRe.MatchString(*body.Name) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", "name must match ^[a-z0-9]+([._-][a-z0-9]+)*$")
			return
		}
	}

	// Validate optional description when provided.
	if body.Description != nil && len(*body.Description) > saMaxDescLen {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "description exceeds maximum length of 280 characters")
		return
	}

	// Validate optional allowed_scopes when provided.
	if body.AllowedScopes != nil {
		if err := validateScopes(*body.AllowedScopes); err != nil {
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
			return
		}
	}

	// Handle the disabled field via SetDisabled; the remaining fields go
	// through the Update path. Both paths are serialised by the DB, so a
	// request that sets both disabled=true and name=... will apply both.
	if body.Disabled != nil {
		// Verify the SA exists in this tenant before calling SetDisabled.
		// SetDisabled validates tenant ownership internally, but we surface the
		// tenant mismatch as a 404 (not a 403) per the spec so callers cannot
		// probe cross-tenant existence.
		if err := h.saService.SetDisabled(r.Context(), saID, tenantID, *body.Disabled, callerID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
				return
			}
			slog.ErrorContext(r.Context(), "sa handler: SetDisabled failed",
				"id", saID,
				"tenant_id", tenantID,
				"err", err,
			)
			writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
			return
		}
	}

	// Apply field-level mutations (name / description / allowed_scopes).
	// When only disabled was changed, Update is still called so we get the
	// refreshed SA back for the response.
	sa, err := h.saService.Update(r.Context(), service.UpdateServiceAccountInput{
		ID:            saID,
		TenantID:      tenantID,
		Name:          body.Name,
		Description:   body.Description,
		AllowedScopes: body.AllowedScopes,
		ActorUserID:   callerID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
			return
		}
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "a service account with that name already exists")
			return
		}
		slog.ErrorContext(r.Context(), "sa handler: Update failed",
			"id", saID,
			"tenant_id", tenantID,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	writeJSON(w, http.StatusOK, saToResponse(sa))
}

// ── DELETE /api/v1/service-accounts/{id} ─────────────────────────────────────

// deleteServiceAccount handles DELETE /api/v1/service-accounts/{id}.
func (h *HTTPHandler) deleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	if !h.requireSAService(w) {
		return
	}
	callerID, tenantID, err := h.requireSAAdmin(w, r)
	if err != nil {
		return
	}

	saID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid service account id")
		return
	}

	// Verify tenant ownership before deleting. Get checks the DB row;
	// if the SA belongs to a different tenant we surface 404 (not 403) so
	// callers cannot probe cross-tenant existence.
	existing, err := h.saService.Get(r.Context(), saID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
			return
		}
		slog.ErrorContext(r.Context(), "sa handler: Get (pre-delete) failed", "id", saID, "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
		return
	}

	if err := h.saService.Delete(r.Context(), saID, callerID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "service account not found")
			return
		}
		slog.ErrorContext(r.Context(), "sa handler: Delete failed",
			"id", saID,
			"tenant_id", tenantID,
			"err", err,
		)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── T14 stubs ─────────────────────────────────────────────────────────────────
// These routes are registered now so the URL space is reserved. T14 fills in
// the bodies. All return 501 NOT_IMPLEMENTED.

func (h *HTTPHandler) saPreflightScopes(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "scope preflight not yet implemented")
}

func (h *HTTPHandler) saListKeys(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "key listing not yet implemented")
}

func (h *HTTPHandler) saIssueKey(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "key issuance not yet implemented")
}

func (h *HTTPHandler) saRevokeKey(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "key revocation not yet implemented")
}

// ── validation helpers ────────────────────────────────────────────────────────

// validateScopes returns an error if any scope value in the list fails the
// allowlist regex ^[a-z][a-z0-9_:]{0,63}$ (CLAUDE.md §7).
func validateScopes(scopes []string) error {
	for _, s := range scopes {
		if !saScpRe.MatchString(s) {
			return errors.New("invalid scope: each scope must match ^[a-z][a-z0-9_:]{0,63}$")
		}
	}
	return nil
}
