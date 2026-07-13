package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// FE-API-034 — SSO admin-config HTTP routes.
//
// A global-admin-only surface over global_sso_config, mounted under the
// auth-owned /api/v1/auth/* prefix (same place as the public provider list).
// The client secret is write-only: GET never returns it (has_secret instead),
// and an empty client_secret on PUT preserves the stored value. SAML editing is
// deferred — the service rejects it with a 400.

// adminProviderItem is the JSON shape returned to the dashboard. It mirrors
// service.AdminProviderView but with wire-format field names + a has_secret bool
// (the ciphertext never crosses the wire).
type adminProviderItem struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	DisplayName   string    `json:"display_name"`
	Enabled       bool      `json:"enabled"`
	OAuthClientID string    `json:"oauth_client_id"`
	OAuthIssuer   string    `json:"oauth_issuer_url"`
	OAuthScopes   []string  `json:"oauth_scopes"`
	HasSecret     bool      `json:"has_secret"`
	AutoProvision bool      `json:"auto_provision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type adminProviderListResponse struct {
	Providers []adminProviderItem `json:"providers"`
}

// putProviderBody is the admin PUT payload. client_secret is write-only: an
// empty value preserves the stored secret on update.
type putProviderBody struct {
	Kind          string   `json:"kind"`
	DisplayName   string   `json:"display_name"`
	Enabled       bool     `json:"enabled"`
	OAuthClientID string   `json:"oauth_client_id"`
	OAuthIssuer   string   `json:"oauth_issuer_url"`
	OAuthScopes   []string `json:"oauth_scopes"`
	ClientSecret  string   `json:"client_secret"`
	AutoProvision bool     `json:"auto_provision"`
}

func toAdminProviderItem(v service.AdminProviderView) adminProviderItem {
	return adminProviderItem{
		ID:            v.ProviderID,
		Kind:          v.Kind,
		DisplayName:   v.DisplayName,
		Enabled:       v.Enabled,
		OAuthClientID: v.OAuthClientID,
		OAuthIssuer:   v.OAuthIssuerURL,
		OAuthScopes:   v.OAuthScopes,
		HasSecret:     v.HasSecret,
		AutoProvision: v.AutoProvision,
		CreatedAt:     v.CreatedAt,
		UpdatedAt:     v.UpdatedAt,
	}
}

// requireGlobalAdmin authenticates the caller and requires users.is_global_admin.
// Returns true only for a human global admin; SA bearers are denied up front
// (Decision #24) and the DB flag is the authoritative gate. Writes the error
// response itself, so callers just `if !h.requireGlobalAdmin(w, r) { return }`.
func (h *HTTPHandler) requireGlobalAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return false
	}
	if claims.PrincipalKind == "service_account" {
		writeError(w, http.StatusForbidden, "DENIED", "global admin required")
		return false
	}
	callerID, err := uuid.Parse(claims.Subject)
	if err != nil {
		slog.ErrorContext(r.Context(), "requireGlobalAdmin: invalid sub in token", "value", claims.Subject)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return false
	}
	user, err := h.svc.GetUserByID(r.Context(), callerID)
	if err != nil || user == nil || !user.IsGlobalAdmin {
		writeError(w, http.StatusForbidden, "DENIED", "global admin required")
		return false
	}
	return true
}

// GET /api/v1/auth/admin/providers — list all configured providers (enabled +
// disabled), secrets masked as has_secret.
func (h *HTTPHandler) listProvidersAdmin(w http.ResponseWriter, r *http.Request) {
	if !h.requireGlobalAdmin(w, r) {
		return
	}
	views, err := h.sso.ListAllProviders(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "sso admin: ListAllProviders", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to list providers")
		return
	}
	resp := adminProviderListResponse{Providers: make([]adminProviderItem, 0, len(views))}
	for _, v := range views {
		resp.Providers = append(resp.Providers, toAdminProviderItem(v))
	}
	writeJSON(w, http.StatusOK, resp)
}

// PUT /api/v1/auth/admin/providers/{provider_id} — create or update one
// provider. The provider id comes from the path; an empty client_secret keeps
// the stored value.
func (h *HTTPHandler) putProviderAdmin(w http.ResponseWriter, r *http.Request) {
	if !h.requireGlobalAdmin(w, r) {
		return
	}
	providerID := r.PathValue("provider_id")

	var body putProviderBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	view, err := h.sso.UpsertProvider(r.Context(), service.UpsertProviderInput{
		ProviderID:     providerID,
		Kind:           body.Kind,
		DisplayName:    body.DisplayName,
		Enabled:        body.Enabled,
		OAuthClientID:  body.OAuthClientID,
		OAuthIssuerURL: body.OAuthIssuer,
		OAuthScopes:    body.OAuthScopes,
		ClientSecret:   body.ClientSecret,
		AutoProvision:  body.AutoProvision,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrProviderConfigInvalid),
			errors.Is(err, service.ErrClientSecretRequired),
			errors.Is(err, service.ErrSAMLNotEditable):
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
		default:
			slog.ErrorContext(r.Context(), "sso admin: UpsertProvider", "err", err, "provider_id", providerID)
			writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to save provider")
		}
		return
	}
	writeJSON(w, http.StatusOK, toAdminProviderItem(*view))
}

// DELETE /api/v1/auth/admin/providers/{provider_id} — remove one provider.
func (h *HTTPHandler) deleteProviderAdmin(w http.ResponseWriter, r *http.Request) {
	if !h.requireGlobalAdmin(w, r) {
		return
	}
	providerID := r.PathValue("provider_id")
	if err := h.sso.DeleteProvider(r.Context(), providerID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: DeleteProvider", "err", err, "provider_id", providerID)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to delete provider")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
