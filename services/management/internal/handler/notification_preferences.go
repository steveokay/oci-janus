// Package handler — notification_preferences.go
//
// FUT-019 Phase 2 — per-user notification preferences. Two routes
// wrap the auditv1 RPCs the FE /settings → Notifications matrix
// drives:
//
//   GET   /api/v1/users/me/notification-preferences
//   PATCH /api/v1/users/me/notification-preferences
//
// Auth posture: the caller's user_id + tenant_id are pulled from the
// JWT (middleware), never from the request body. The gRPC handler
// trusts those values; the BFF is the gate that maps JWT → user
// identity.
//
// Default model: a row that doesn't exist in the DB defaults to
// bell=on / email=off / webhook=off. The BFF merges the DB rows
// against the known-category list so the FE always sees one row per
// category — fewer NULL checks on the FE.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// knownNotificationCategories is the FE-visible category catalogue.
// FUT-019 Phase 2 ships scanner_freshness as the only category with a
// real schedule; the others render greyed "ships with category" rows
// so the operator can see the roadmap on day one without the matrix
// looking empty. Adding a new category lands one row here + the
// scheduler Category implementation on the audit side.
var knownNotificationCategories = []NotificationCategoryMeta{
	{Key: "scanner_freshness", Label: "Scanner freshness", Description: "Periodic reminders when your Trivy adapter is behind the latest known release.", ShippedIn: "FUT-019 Phase 2"},
	{Key: "invite_expiry_warning", Label: "Invite expiry", Description: "Ping the inviter N days before a pending invite token expires.", ShippedIn: "FUT-019 Phase 3"},
	{Key: "cert_expiry_warning", Label: "Certificate expiry", Description: "mTLS / TLS certificates within 14 days of expiry. Critical for production.", ShippedIn: "FUT-019 Phase 3"},
	{Key: "password_rotation_reminder", Label: "Password rotation", Description: "90-day cadence personal nudge to rotate the account password.", ShippedIn: "FUT-019 Phase 3"},
	{Key: "retention_dry_run_summary", Label: "Retention dry-run summary", Description: "Weekly digest of what your retention rules would delete if grace fired now.", ShippedIn: "FUT-019 Phase 3"},
	{Key: "failed_login_burst", Label: "Failed-login bursts", Description: "N failed logins inside M minutes from a single IP / user. Elevates an existing audit event.", ShippedIn: "FUT-019 Phase 3"},
	{Key: "plan_quota_threshold", Label: "Plan / quota threshold", Description: "Fires when your tenant hits 80% storage or pull quota.", ShippedIn: "FUT-019 Phase 3"},
}

// NotificationCategoryMeta is the FE-rendered context block for a
// category. Shipping the description + shipped-in marker over the
// wire keeps the FE matrix self-documenting — the operator sees what
// each row does without leaving the page.
type NotificationCategoryMeta struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	ShippedIn   string `json:"shipped_in"`
}

// NotificationPreferenceRow is one row in the FE matrix. Merges the
// DB row (if any) with defaults so the FE always sees every category.
type NotificationPreferenceRow struct {
	NotificationCategoryMeta
	BellEnabled    bool `json:"bell_enabled"`
	EmailEnabled   bool `json:"email_enabled"`
	WebhookEnabled bool `json:"webhook_enabled"`
}

// NotificationPreferencesResponse is the top-level JSON envelope.
type NotificationPreferencesResponse struct {
	Preferences []NotificationPreferenceRow `json:"preferences"`
}

// PatchNotificationPreferencesBody is the wire shape of the PATCH
// body. Each row carries the category key + the three channel flags;
// the BFF upserts every row. Empty categories on the way in are
// rejected with 400.
type PatchNotificationPreferencesBody struct {
	Preferences []PatchPreferenceRow `json:"preferences"`
}

// PatchPreferenceRow is one row in the PATCH body.
type PatchPreferenceRow struct {
	Category       string `json:"category"`
	BellEnabled    bool   `json:"bell_enabled"`
	EmailEnabled   bool   `json:"email_enabled"`
	WebhookEnabled bool   `json:"webhook_enabled"`
}

// handleGetNotificationPreferences serves
// GET /api/v1/users/me/notification-preferences.
//
// Returns one row per known category. DB rows that exist override the
// defaults (bell=on / email=off / webhook=off); categories with no row
// fall back. Always 200 OK with a complete matrix — the FE never has
// to handle a partial response shape.
func (h *Handler) handleGetNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	resp, err := h.audit.GetUserNotificationPreferences(r.Context(), &auditv1.GetUserNotificationPreferencesRequest{
		TenantId: tenantID,
		UserId:   userID,
	})
	if err != nil {
		slog.Error("GetUserNotificationPreferences", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load preferences")
		return
	}
	dbByCategory := make(map[string]*auditv1.NotificationPreference, len(resp.GetPreferences()))
	for _, p := range resp.GetPreferences() {
		dbByCategory[p.GetCategory()] = p
	}
	out := NotificationPreferencesResponse{
		Preferences: make([]NotificationPreferenceRow, 0, len(knownNotificationCategories)),
	}
	for _, cat := range knownNotificationCategories {
		row := NotificationPreferenceRow{
			NotificationCategoryMeta: cat,
			// Defaults: bell on, email + webhook off.
			BellEnabled: true,
		}
		if pref, ok := dbByCategory[cat.Key]; ok {
			row.BellEnabled = pref.GetBellEnabled()
			row.EmailEnabled = pref.GetEmailEnabled()
			row.WebhookEnabled = pref.GetWebhookEnabled()
		}
		out.Preferences = append(out.Preferences, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePatchNotificationPreferences serves
// PATCH /api/v1/users/me/notification-preferences.
//
// Accepts a full set of category rows + upserts each. Rejects rows
// with unknown category keys so a typo can't silently land in the
// preferences table. Returns the resulting full matrix (same shape as
// GET) so the FE can refresh its cache without a follow-up Get.
func (h *Handler) handlePatchNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body PatchNotificationPreferencesBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Preferences) == 0 {
		writeError(w, http.StatusBadRequest, "preferences array is required")
		return
	}
	knownKeys := make(map[string]struct{}, len(knownNotificationCategories))
	for _, c := range knownNotificationCategories {
		knownKeys[c.Key] = struct{}{}
	}
	patchProtos := make([]*auditv1.NotificationPreference, 0, len(body.Preferences))
	for _, row := range body.Preferences {
		if _, ok := knownKeys[row.Category]; !ok {
			writeError(w, http.StatusBadRequest, "unknown category: "+row.Category)
			return
		}
		patchProtos = append(patchProtos, &auditv1.NotificationPreference{
			Category:       row.Category,
			BellEnabled:    row.BellEnabled,
			EmailEnabled:   row.EmailEnabled,
			WebhookEnabled: row.WebhookEnabled,
		})
	}
	if _, err := h.audit.UpdateUserNotificationPreferences(r.Context(), &auditv1.UpdateUserNotificationPreferencesRequest{
		TenantId:    tenantID,
		UserId:      userID,
		Preferences: patchProtos,
	}); err != nil {
		slog.Error("UpdateUserNotificationPreferences", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save preferences")
		return
	}
	// Re-fetch + merge for the response. Cheaper than reshaping in
	// Go — the audit DB round-trip is ~1ms.
	h.handleGetNotificationPreferences(w, r)
}
