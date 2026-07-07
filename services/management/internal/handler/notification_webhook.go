// Package handler — notification_webhook.go
//
// FUT-019 webhook notification channel (BFF surface). Three admin routes front
// the auditv1 webhook config RPCs:
//
//	GET  /api/v1/notifications/webhook-config       (admin) → GetNotificationWebhookConfig
//	PUT  /api/v1/notifications/webhook-config       (admin) → PutNotificationWebhookConfig
//	POST /api/v1/notifications/webhook-config/test  (admin) → SendTestNotificationWebhook
//
// Auth posture mirrors the email transport routes: platform-admin primitive
// required AND service-account bearers denied (a deployment-wide config change
// must never clear the gate via an SA token, Decision #24). The HMAC secret is
// write-only — the GET returns has_secret only; an empty secret on PUT keeps the
// stored value. FailedPrecondition → 409 (reuses writeGRPCError from
// email_transport.go).
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// notificationWebhookJSON is the wire shape for the GET / PUT config routes.
// It mirrors the proto NotificationWebhookConfig but carries only the
// has_secret marker — the raw HMAC secret is never echoed back.
type notificationWebhookJSON struct {
	URL               string   `json:"url"`
	Enabled           bool     `json:"enabled"`
	HasSecret         bool     `json:"has_secret"`
	EnabledCategories []string `json:"enabled_categories"`
	LastTestAt        string   `json:"last_test_at,omitempty"`
	LastTestOK        bool     `json:"last_test_ok"`
	LastTestError     string   `json:"last_test_error,omitempty"`
}

// notificationWebhookPutBody is the JSON body for the PUT config route. The
// secret field follows the keep-existing convention: an empty string leaves
// the stored secret untouched; a non-empty string replaces it.
type notificationWebhookPutBody struct {
	URL               string   `json:"url"`
	Enabled           bool     `json:"enabled"`
	Secret            string   `json:"secret"` // empty = keep existing
	EnabledCategories []string `json:"enabled_categories"`
}

// notificationWebhookToJSON maps the proto config to its wire shape, dropping
// the raw secret (only has_secret survives). EnabledCategories is normalised
// to a non-nil slice so an empty set serialises as `[]` rather than `null`.
func notificationWebhookToJSON(c *auditv1.NotificationWebhookConfig) notificationWebhookJSON {
	out := notificationWebhookJSON{
		URL:               c.GetUrl(),
		Enabled:           c.GetEnabled(),
		HasSecret:         c.GetHasSecret(),
		EnabledCategories: c.GetEnabledCategories(),
		LastTestOK:        c.GetLastTestOk(),
		LastTestError:     c.GetLastTestError(),
	}
	if out.EnabledCategories == nil {
		out.EnabledCategories = []string{}
	}
	if t := c.GetLastTestAt(); t != nil {
		out.LastTestAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

// handleGetNotificationWebhook serves GET /api/v1/notifications/webhook-config.
// Admin-only; returns the current webhook config with the secret masked.
func (h *Handler) handleGetNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) { // same admin+SA gate as the email transport routes
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.GetNotificationWebhookConfig(r.Context(), &auditv1.GetNotificationWebhookConfigRequest{TenantId: tenantID})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationWebhookToJSON(resp))
}

// handlePutNotificationWebhook serves PUT /api/v1/notifications/webhook-config.
// Admin-only; upserts the webhook config. An empty secret keeps the stored
// value; the response echoes the masked config back.
func (h *Handler) handlePutNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body notificationWebhookPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	// Normalise a nil categories slice to empty so the audit service always
	// receives an explicit (possibly empty) set rather than a nil sentinel.
	cats := body.EnabledCategories
	if cats == nil {
		cats = []string{}
	}
	resp, err := h.audit.PutNotificationWebhookConfig(r.Context(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId:  tenantID,
		UpdatedBy: userID,
		Url:       body.URL,
		Enabled:   body.Enabled,
		// Secret: empty means "keep existing"; a value replaces it.
		Secret:            body.Secret,
		EnabledCategories: cats,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationWebhookToJSON(resp))
}

// handleTestNotificationWebhook serves
// POST /api/v1/notifications/webhook-config/test. Admin-only; fires a signed
// test delivery at the configured URL and returns {ok, error}.
func (h *Handler) handleTestNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.SendTestNotificationWebhook(r.Context(), &auditv1.SendTestNotificationWebhookRequest{TenantId: tenantID})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": resp.GetOk(), "error": resp.GetError()})
}
