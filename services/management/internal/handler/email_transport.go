// Package handler — email_transport.go
//
// FUT-019 Phase 3 — email notification transport (BFF surface). Four
// REST routes front the auditv1 email RPCs plus the authv1
// ResolveUserEmails RPC:
//
//	GET  /api/v1/notifications/email-transport        (admin) → GetEmailTransportConfig
//	PUT  /api/v1/notifications/email-transport        (admin) → PutEmailTransportConfig
//	POST /api/v1/notifications/email-transport/test   (admin) → SendTestEmail (caller's own email)
//	GET  /api/v1/notifications/email-deliveries       (user)  → ListEmailDeliveries (own rows)
//
// Auth posture:
//   - The three transport-config routes require the platform-admin
//     primitive (users.is_global_admin) AND deny service-account
//     bearers — changing the transport is a deployment-wide config
//     change, so an SA token whose owner is an admin must not clear
//     the gate (Decision #24).
//   - The delivery-log route is open to any logged-in user but the
//     user_id is forced from the JWT (never trusted from the client),
//     so a caller can only ever read their own delivery rows.
//
// Secret handling: the config GET/PUT responses map from the proto
// EmailTransportConfig, which carries has_resend_key / has_smtp_password
// booleans only — the raw Resend API key and SMTP password are never
// echoed back over the wire. On the PUT path an empty secret field
// means "keep the stored value"; a non-empty value replaces it.
//
// "Email not configured" surfaces as a gRPC FailedPrecondition from the
// audit service, which writeGRPCError maps to HTTP 409 so the FE can
// render the "set up a transport" empty state rather than an error.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// emailTransportJSON is the wire shape returned by the GET / PUT config
// routes. It mirrors the proto EmailTransportConfig but deliberately
// carries only the has_* booleans for secrets — the Resend key and SMTP
// password themselves are never serialised.
type emailTransportJSON struct {
	Provider        string `json:"provider"`
	Enabled         bool   `json:"enabled"`
	FromAddress     string `json:"from_address"`
	FromName        string `json:"from_name"`
	SMTPHost        string `json:"smtp_host"`
	SMTPPort        int32  `json:"smtp_port"`
	SMTPUsername    string `json:"smtp_username"`
	SMTPTLSMode     string `json:"smtp_tls_mode"`
	HasResendKey    bool   `json:"has_resend_key"`
	HasSMTPPassword bool   `json:"has_smtp_password"`
	LastTestAt      string `json:"last_test_at,omitempty"`
	LastTestOK      bool   `json:"last_test_ok"`
	LastTestError   string `json:"last_test_error,omitempty"`
}

// emailTransportPutBody is the JSON body for the PUT config route. The
// two secret fields (resend_api_key / smtp_password) follow the
// keep-existing convention: an empty string leaves the stored secret
// untouched, a non-empty string replaces it.
type emailTransportPutBody struct {
	Provider     string `json:"provider"`
	Enabled      bool   `json:"enabled"`
	FromAddress  string `json:"from_address"`
	FromName     string `json:"from_name"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPTLSMode  string `json:"smtp_tls_mode"`
	ResendAPIKey string `json:"resend_api_key"`
	SMTPPassword string `json:"smtp_password"`
}

// emailDeliveryJSON is one row in the per-user delivery log.
type emailDeliveryJSON struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Subject   string `json:"subject"`
	ToAddress string `json:"to_address"`
	Status    string `json:"status"`
	LastError string `json:"last_error"`
	CreatedAt string `json:"created_at,omitempty"`
	SentAt    string `json:"sent_at,omitempty"`
}

// emailDeliveriesJSON is the envelope for the delivery-log route. The
// deliveries slice is always non-nil so the FE never has to guard a
// null.
type emailDeliveriesJSON struct {
	Deliveries []emailDeliveryJSON `json:"deliveries"`
}

// emailTransportToJSON maps the proto config to its wire shape, dropping
// every raw secret (only the has_* markers survive).
func emailTransportToJSON(c *auditv1.EmailTransportConfig) emailTransportJSON {
	out := emailTransportJSON{
		Provider:        c.GetProvider(),
		Enabled:         c.GetEnabled(),
		FromAddress:     c.GetFromAddress(),
		FromName:        c.GetFromName(),
		SMTPHost:        c.GetSmtpHost(),
		SMTPPort:        c.GetSmtpPort(),
		SMTPUsername:    c.GetSmtpUsername(),
		SMTPTLSMode:     c.GetSmtpTlsMode(),
		HasResendKey:    c.GetHasResendKey(),
		HasSMTPPassword: c.GetHasSmtpPassword(),
		LastTestOK:      c.GetLastTestOk(),
		LastTestError:   c.GetLastTestError(),
	}
	if t := c.GetLastTestAt(); t != nil {
		out.LastTestAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

// emailDeliveriesToJSON maps the proto delivery list to its wire shape.
// The slice is pre-allocated (never nil) so an empty log serialises as
// `[]` rather than `null`.
func emailDeliveriesToJSON(resp *auditv1.ListEmailDeliveriesResponse) emailDeliveriesJSON {
	out := emailDeliveriesJSON{
		Deliveries: make([]emailDeliveryJSON, 0, len(resp.GetDeliveries())),
	}
	for _, d := range resp.GetDeliveries() {
		row := emailDeliveryJSON{
			ID:        d.GetId(),
			Category:  d.GetCategory(),
			Subject:   d.GetSubject(),
			ToAddress: d.GetToAddress(),
			Status:    d.GetStatus(),
			LastError: d.GetLastError(),
		}
		if t := d.GetCreatedAt(); t != nil {
			row.CreatedAt = t.AsTime().UTC().Format(time.RFC3339)
		}
		if t := d.GetSentAt(); t != nil {
			row.SentAt = t.AsTime().UTC().Format(time.RFC3339)
		}
		out.Deliveries = append(out.Deliveries, row)
	}
	return out
}

// writeGRPCError translates a gRPC status into an HTTP error response.
// FailedPrecondition (e.g. "email transport not configured") maps to
// 409 so the FE can render the "not set up" empty state; the remaining
// well-known codes map onto their conventional HTTP equivalents, and
// anything else falls through to 500. Messages are intentionally
// generic — internal detail (service names, gRPC codes) must never leak
// to the client per CLAUDE.md §4.13.
func writeGRPCError(w http.ResponseWriter, err error) {
	switch status.Code(err) {
	case codes.FailedPrecondition:
		writeError(w, http.StatusConflict, "precondition not met")
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "not found")
	case codes.InvalidArgument:
		writeError(w, http.StatusBadRequest, "invalid request")
	case codes.PermissionDenied:
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// requireEmailAdmin gates the transport-config routes to platform
// admins and blocks service-account bearers. Returns false (and writes
// the response) when the caller is denied — the handler must return
// immediately. Mirrors requireScannerAdmin minus the client-nil branch
// (h.audit is a required, always-present dependency).
func (h *Handler) requireEmailAdmin(w http.ResponseWriter, r *http.Request) bool {
	// SA bearers can never change a deployment-wide config, even when the
	// owning human is a global admin (Decision #24).
	if middleware.PrincipalKindFromContext(r.Context()) == middleware.PrincipalKindServiceAccount {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	return true
}

// resolveCallerEmail asks the auth service for the caller's own email
// address. Returns an empty string (no error) when the account has no
// resolvable email so the test-send handler can surface a clean 400.
func (h *Handler) resolveCallerEmail(ctx context.Context, tenantID, userID string) (string, error) {
	resp, err := h.auth.ResolveUserEmails(ctx, &authv1.ResolveUserEmailsRequest{
		TenantId: tenantID,
		UserIds:  []string{userID},
	})
	if err != nil {
		return "", err
	}
	emails := resp.GetEmails()
	if len(emails) == 0 {
		return "", nil
	}
	return emails[0].GetEmail(), nil
}

// handleGetEmailTransport serves GET /api/v1/notifications/email-transport.
// Admin-only; returns the current transport config with secrets masked.
func (h *Handler) handleGetEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.GetEmailTransportConfig(r.Context(), &auditv1.GetEmailTransportConfigRequest{
		TenantId: tenantID,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailTransportToJSON(resp))
}

// handlePutEmailTransport serves PUT /api/v1/notifications/email-transport.
// Admin-only; upserts the transport config. Empty secret fields keep the
// stored value; the response echoes the masked config back.
func (h *Handler) handlePutEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body emailTransportPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	resp, err := h.audit.PutEmailTransportConfig(r.Context(), &auditv1.PutEmailTransportConfigRequest{
		TenantId:     tenantID,
		UpdatedBy:    userID,
		Provider:     body.Provider,
		Enabled:      body.Enabled,
		FromAddress:  body.FromAddress,
		FromName:     body.FromName,
		SmtpHost:     body.SMTPHost,
		SmtpPort:     int32(body.SMTPPort),
		SmtpUsername: body.SMTPUsername,
		SmtpTlsMode:  body.SMTPTLSMode,
		// Secrets: empty means "keep existing"; a value replaces it.
		ResendApiKey: body.ResendAPIKey,
		SmtpPassword: body.SMTPPassword,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailTransportToJSON(resp))
}

// handleTestEmailTransport serves POST /api/v1/notifications/email-transport/test.
// Admin-only; sends a test email to the CALLER's own resolved address —
// never an arbitrary recipient supplied by the client.
func (h *Handler) handleTestEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	// Resolve the caller's own email via auth so a client cannot direct
	// the test send at an address it doesn't own.
	email, err := h.resolveCallerEmail(r.Context(), tenantID, userID)
	if err != nil || email == "" {
		writeError(w, http.StatusBadRequest, "your account has no email address to test with")
		return
	}
	resp, err := h.audit.SendTestEmail(r.Context(), &auditv1.SendTestEmailRequest{
		TenantId:  tenantID,
		ToAddress: email,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": resp.GetOk(), "error": resp.GetError()})
}

// handleListEmailDeliveries serves GET /api/v1/notifications/email-deliveries.
// Open to any logged-in user, but the user_id is forced from the JWT so
// a caller can only ever read their own delivery rows.
func (h *Handler) handleListEmailDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	resp, err := h.audit.ListEmailDeliveries(r.Context(), &auditv1.ListEmailDeliveriesRequest{
		TenantId: tenantID,
		UserId:   userID,
		PageSize: 25,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailDeliveriesToJSON(resp))
}
