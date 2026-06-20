// FE-API-034 — Admin CRUD for SSO provider configuration.
//
// These routes live on services/auth because the data — auth_providers and
// auth_login_sessions — is owned by the auth DB. A tenant admin (any user
// holding `admin` or `owner` somewhere in the tenant) may configure their
// own tenant's providers; a platform admin (org=*, admin in the platform
// admin tenant) may target any tenant via ?tenant_id=.
//
// Plaintext client_secret only ever travels in the request body. It is
// AES-256-GCM encrypted before persistence and never returned to the wire.
// Audit events (auth.provider_created/updated/deleted) flow through the
// shared RabbitMQ publisher so registry-audit picks them up — payloads are
// deliberately small and never contain the client_secret.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// Audit routing keys for the SSO provider CRUD. Declared here rather than
// in libs/rabbitmq/events because the audit consumer treats all auth.*
// events generically (routing key + payload JSON); a follow-up commit can
// promote these to typed payloads.
const (
	routingAuthProviderCreated = "auth.provider_created"
	routingAuthProviderUpdated = "auth.provider_updated"
	routingAuthProviderDeleted = "auth.provider_deleted"
)

// adminProviderResponse is the JSON shape returned by every admin CRUD
// endpoint. ClientSecret-related fields are intentionally absent — the
// ciphertext never leaves the server and the plaintext is only ever
// accepted on the inbound request.
type adminProviderResponse struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Type          string    `json:"type"`
	DisplayName   string    `json:"display_name"`
	Enabled       bool      `json:"enabled"`
	OAuthClientID string    `json:"oauth_client_id,omitempty"`
	OAuthIssuer   string    `json:"oauth_issuer_url,omitempty"`
	OAuthScopes   []string  `json:"oauth_scopes,omitempty"`

	SAMLEntityID string `json:"saml_entity_id,omitempty"`
	SAMLAudience string `json:"saml_audience,omitempty"`
	// SAML IdP metadata XML is returned to the admin so they can confirm
	// what's been configured. It contains no secret material (it's the
	// public IdP certificate + endpoint URLs).
	SAMLIdpMetadataXML string `json:"saml_idp_metadata_xml,omitempty"`

	AutoProvision bool      `json:"auto_provision"`
	DefaultRole   string    `json:"default_role"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	UpdatedBy     string    `json:"updated_by,omitempty"`
}

// providerToAdminResp sanitises a repository.AuthProvider into the public
// admin response. Always strips OAuthClientSecretEnc — a belt-and-braces
// check, since the service layer also strips it on list paths.
func providerToAdminResp(p *repository.AuthProvider) adminProviderResponse {
	resp := adminProviderResponse{
		ID:                 p.ID.String(),
		TenantID:           p.TenantID.String(),
		Type:               string(p.Type),
		DisplayName:        p.DisplayName,
		Enabled:            p.Enabled,
		OAuthClientID:      p.OAuthClientID,
		OAuthIssuer:        p.OAuthIssuerURL,
		OAuthScopes:        p.OAuthScopes,
		SAMLEntityID:       p.SAMLEntityID,
		SAMLAudience:       p.SAMLAudience,
		SAMLIdpMetadataXML: p.SAMLIdpMetadataXML,
		AutoProvision:      p.AutoProvision,
		DefaultRole:        p.DefaultRole,
		CreatedAt:          p.CreatedAt,
		UpdatedAt:          p.UpdatedAt,
	}
	if p.UpdatedBy != nil {
		resp.UpdatedBy = p.UpdatedBy.String()
	}
	return resp
}

// ── Authorization gate ──────────────────────────────────────────────────────

// authorizeProviderAdmin enforces the per-tenant admin gate. The caller
// must hold admin or owner anywhere in the target tenant. When ?tenant_id=
// differs from the caller's tenant, the caller must additionally hold the
// platform-admin marker grant (org=*, admin) in the platform admin tenant
// — but services/auth does not know its platform admin tenant ID, so we
// approximate by allowing cross-tenant access only when the caller's JWT
// shows the marker scope explicitly.
//
// Returns the resolved target tenant or zero UUID + an HTTP error code +
// message. The caller is expected to write the response and return.
func (h *HTTPHandler) authorizeProviderAdmin(r *http.Request) (callerID, callerTenant, targetTenant uuid.UUID, status int, msg string) {
	claims, err := h.requireAuth(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusUnauthorized, "authentication required"
	}
	cid, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusInternalServerError, "invalid user in token"
	}
	ct, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusInternalServerError, "invalid tenant in token"
	}

	target := ct
	if raw := r.URL.Query().Get("tenant_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusBadRequest, "invalid tenant_id"
		}
		target = parsed
	}

	// Same tenant → tenant-admin role within the target tenant is enough.
	// Different tenant → require the platform-admin marker; the only way
	// to hold that marker is via the seed migration in registry-auth.
	assignments, err := h.svc.GetUserRoles(r.Context(), cid, ct)
	if err != nil {
		slog.WarnContext(r.Context(), "sso admin: GetUserRoles", "err", err)
		return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusInternalServerError, "internal error"
	}

	if target == ct {
		for _, a := range assignments {
			if a.RoleName == "admin" || a.RoleName == "owner" {
				return cid, ct, target, 0, ""
			}
		}
		return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusForbidden, "tenant admin required"
	}

	// Cross-tenant: caller must hold the platform-admin marker (org=*, admin).
	for _, a := range assignments {
		if a.ScopeType == "org" && a.ScopeValue == "*" &&
			(a.RoleName == "admin" || a.RoleName == "owner") {
			return cid, ct, target, 0, ""
		}
	}
	return uuid.Nil, uuid.Nil, uuid.Nil, http.StatusForbidden, "platform-admin role required for cross-tenant configuration"
}

// ── Routes ──────────────────────────────────────────────────────────────────

// GET /api/v1/admin/auth-providers?tenant_id=
//
// Lists every provider (enabled + disabled) for the target tenant.
// ClientSecret ciphertext is stripped at the service layer; this handler
// re-strips defensively before serialising.
func (h *HTTPHandler) adminListAuthProviders(w http.ResponseWriter, r *http.Request) {
	_, _, target, status, msg := h.authorizeProviderAdmin(r)
	if status != 0 {
		writeError(w, status, httpCodeFor(status), msg)
		return
	}
	providers, err := h.sso.ListAllProviders(r.Context(), target)
	if err != nil {
		slog.ErrorContext(r.Context(), "sso admin: ListAllProviders", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	out := make([]adminProviderResponse, 0, len(providers))
	for _, p := range providers {
		// Defence in depth: drop ciphertext at the handler boundary too.
		p.OAuthClientSecretEnc = nil
		out = append(out, providerToAdminResp(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// adminCreateProviderBody mirrors the validated input shape consumed by
// service.SSO.CreateProvider but with all fields as plain JSON types.
type adminCreateProviderBody struct {
	TenantID           string   `json:"tenant_id"`
	Type               string   `json:"type"`
	DisplayName        string   `json:"display_name"`
	Enabled            *bool    `json:"enabled,omitempty"`
	OAuthClientID      string   `json:"oauth_client_id,omitempty"`
	OAuthClientSecret  string   `json:"oauth_client_secret,omitempty"`
	OAuthIssuerURL     string   `json:"oauth_issuer_url,omitempty"`
	OAuthScopes        []string `json:"oauth_scopes,omitempty"`
	SAMLIdpMetadataXML string   `json:"saml_idp_metadata_xml,omitempty"`
	SAMLEntityID       string   `json:"saml_entity_id,omitempty"`
	SAMLAudience       string   `json:"saml_audience,omitempty"`
	AutoProvision      *bool    `json:"auto_provision,omitempty"`
	DefaultRole        string   `json:"default_role,omitempty"`
}

// POST /api/v1/admin/auth-providers
func (h *HTTPHandler) adminCreateAuthProvider(w http.ResponseWriter, r *http.Request) {
	callerID, _, target, status, msg := h.authorizeProviderAdmin(r)
	if status != 0 {
		writeError(w, status, httpCodeFor(status), msg)
		return
	}
	var body adminCreateProviderBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB for SAML XML payloads
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	// Body's tenant_id, if supplied, must match the resolved target so an
	// admin cannot create a provider in a tenant they did not authorize for.
	if body.TenantID != "" && body.TenantID != target.String() {
		writeError(w, http.StatusForbidden, "DENIED", "tenant_id mismatch")
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	autoProvision := true
	if body.AutoProvision != nil {
		autoProvision = *body.AutoProvision
	}
	defaultRole := body.DefaultRole
	if defaultRole == "" {
		defaultRole = "reader"
	}

	created, err := h.sso.CreateProvider(r.Context(), service.CreateProviderInput{
		TenantID:           target,
		Type:               repository.AuthProviderType(body.Type),
		DisplayName:        body.DisplayName,
		Enabled:            enabled,
		OAuthClientID:      body.OAuthClientID,
		OAuthClientSecret:  body.OAuthClientSecret,
		OAuthIssuerURL:     body.OAuthIssuerURL,
		OAuthScopes:        body.OAuthScopes,
		SAMLIdpMetadataXML: body.SAMLIdpMetadataXML,
		SAMLEntityID:       body.SAMLEntityID,
		SAMLAudience:       body.SAMLAudience,
		AutoProvision:      autoProvision,
		DefaultRole:        defaultRole,
		UpdatedBy:          &callerID,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidProviderConfig) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
			return
		}
		if errors.Is(err, repository.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "CONFLICT", "provider of this type already exists for the tenant")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: CreateProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	// Strip ciphertext before serialising or publishing the audit event.
	created.OAuthClientSecretEnc = nil
	h.publishProviderEvent(r.Context(), routingAuthProviderCreated, created, callerID)
	writeJSON(w, http.StatusCreated, providerToAdminResp(created))
}

// GET /api/v1/admin/auth-providers/{id}
func (h *HTTPHandler) adminGetAuthProvider(w http.ResponseWriter, r *http.Request) {
	_, _, target, status, msg := h.authorizeProviderAdmin(r)
	if status != 0 {
		writeError(w, status, httpCodeFor(status), msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid id")
		return
	}
	p, err := h.sso.GetProvider(r.Context(), id, false, false)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: GetProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	// Authorization fences off other tenants — never reveal that a provider
	// in a different tenant exists.
	if p.TenantID != target {
		writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, providerToAdminResp(p))
}

// adminUpdateProviderBody is the PATCH payload. Fields are pointers so the
// handler can distinguish "absent" from "set to empty". OAuthClientSecret is
// a pointer to a string so a non-nil pointer to "" clears the secret.
type adminUpdateProviderBody struct {
	DisplayName        *string   `json:"display_name,omitempty"`
	Enabled            *bool     `json:"enabled,omitempty"`
	OAuthClientID      *string   `json:"oauth_client_id,omitempty"`
	OAuthClientSecret  *string   `json:"oauth_client_secret,omitempty"`
	OAuthIssuerURL     *string   `json:"oauth_issuer_url,omitempty"`
	OAuthScopes        *[]string `json:"oauth_scopes,omitempty"`
	SAMLIdpMetadataXML *string   `json:"saml_idp_metadata_xml,omitempty"`
	SAMLEntityID       *string   `json:"saml_entity_id,omitempty"`
	SAMLAudience       *string   `json:"saml_audience,omitempty"`
	AutoProvision      *bool     `json:"auto_provision,omitempty"`
	DefaultRole        *string   `json:"default_role,omitempty"`
}

// PATCH /api/v1/admin/auth-providers/{id}
func (h *HTTPHandler) adminUpdateAuthProvider(w http.ResponseWriter, r *http.Request) {
	callerID, _, target, status, msg := h.authorizeProviderAdmin(r)
	if status != 0 {
		writeError(w, status, httpCodeFor(status), msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid id")
		return
	}

	// Confirm the row belongs to the target tenant before mutating it.
	existing, err := h.sso.GetProvider(r.Context(), id, false, false)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: GetProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if existing.TenantID != target {
		writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
		return
	}

	var body adminUpdateProviderBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid request body")
		return
	}

	updated, err := h.sso.UpdateProvider(r.Context(), id, service.UpdateProviderInput{
		DisplayName:        body.DisplayName,
		Enabled:            body.Enabled,
		OAuthClientID:      body.OAuthClientID,
		OAuthClientSecret:  body.OAuthClientSecret,
		OAuthIssuerURL:     body.OAuthIssuerURL,
		OAuthScopes:        body.OAuthScopes,
		SAMLIdpMetadataXML: body.SAMLIdpMetadataXML,
		SAMLEntityID:       body.SAMLEntityID,
		SAMLAudience:       body.SAMLAudience,
		AutoProvision:      body.AutoProvision,
		DefaultRole:        body.DefaultRole,
		UpdatedBy:          &callerID,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidProviderConfig) {
			writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: UpdateProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}

	updated.OAuthClientSecretEnc = nil
	h.publishProviderEvent(r.Context(), routingAuthProviderUpdated, updated, callerID)
	writeJSON(w, http.StatusOK, providerToAdminResp(updated))
}

// DELETE /api/v1/admin/auth-providers/{id}
func (h *HTTPHandler) adminDeleteAuthProvider(w http.ResponseWriter, r *http.Request) {
	callerID, _, target, status, msg := h.authorizeProviderAdmin(r)
	if status != 0 {
		writeError(w, status, httpCodeFor(status), msg)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid id")
		return
	}
	existing, err := h.sso.GetProvider(r.Context(), id, false, false)
	if err != nil {
		if errors.Is(err, service.ErrProviderNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: GetProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if existing.TenantID != target {
		writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
		return
	}
	if err := h.sso.DeleteProvider(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOTFOUND", "provider not found")
			return
		}
		slog.ErrorContext(r.Context(), "sso admin: DeleteProvider", "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	h.publishProviderEvent(r.Context(), routingAuthProviderDeleted, existing, callerID)
	w.WriteHeader(http.StatusNoContent)
}

// publishProviderEvent emits an audit event for the given mutation. The
// payload deliberately excludes the OAuth client_secret (plaintext OR
// ciphertext) — the routing key + provider identity is enough for an audit
// trail. Publish failures are logged, not surfaced to the caller, because
// the DB mutation has already committed.
func (h *HTTPHandler) publishProviderEvent(ctx context.Context, routingKey string, p *repository.AuthProvider, actor uuid.UUID) {
	if h.eventPublisher == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":     p.TenantID.String(),
		"provider_id":   p.ID.String(),
		"provider_type": string(p.Type),
		"display_name":  p.DisplayName,
		"enabled":       p.Enabled,
		"actor_id":      actor.String(),
	})
	if err != nil {
		slog.WarnContext(ctx, "sso admin: marshal provider event", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       routingKey,
		TenantID:   p.TenantID.String(),
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.eventPublisher.Publish(ctx, routingKey, evt); err != nil {
		slog.WarnContext(ctx, "sso admin: publish provider event", "err", err, "key", routingKey)
	}
}

// httpCodeFor returns the canonical token-style code string for an HTTP
// status. Keeps writeError calls grammatical without sprinkling magic
// strings through the handlers.
func httpCodeFor(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "DENIED"
	case http.StatusNotFound:
		return "NOTFOUND"
	case http.StatusBadRequest:
		return "BADREQUEST"
	case http.StatusConflict:
		return "CONFLICT"
	}
	return "INTERNAL"
}
