// Package handler — workspace_audit_export.go
//
// Futures.md Tier 1 #4 — audit log streaming to SIEM.
//
// Four HTTP routes that wrap the AuditService audit-export RPCs
// behind a workspace-admin gate. Mirrors the workspace_domains.go
// shape: same `requireDomainAdmin` posture (tenant-admin or
// platform-admin only — Review §A1 Top-5 #2 fix), same
// opt-in-when-client-unwired posture (returns 404 if h.audit is
// nil — although in practice the audit gRPC dial is always present
// because Build History + Activity ride on it too).
//
//	GET    /api/v1/workspace/me/audit-export        — fetch config
//	PUT    /api/v1/workspace/me/audit-export        — upsert config
//	DELETE /api/v1/workspace/me/audit-export        — clear config
//	POST   /api/v1/workspace/me/audit-export/test   — fire synthetic event
//
// The PUT/DELETE/test paths require tenant-admin (or platform-admin)
// — sending audit events to an external destination is a
// security-relevant policy decision affecting the entire tenant's
// audit trail, same shape as adding a custom domain or rotating an
// SSO client_secret.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuditExportConfigResponse is the JSON wire form. Mirrors the proto
// 1:1 except timestamps render as ISO-8601 strings to keep the FE
// hooks consistent with the rest of the BFF.
type AuditExportConfigResponse struct {
	ID               string  `json:"id"`
	Enabled          bool    `json:"enabled"`
	Format           string  `json:"format"`
	TargetURL        string  `json:"target_url"`
	HMACSecretSet    bool    `json:"hmac_secret_set"`
	BearerTokenSet   bool    `json:"bearer_token_set"`
	EventFiltersJSON string  `json:"event_filters_json,omitempty"`
	LastSuccessAt    *string `json:"last_success_at,omitempty"`
	LastAttemptAt    *string `json:"last_attempt_at,omitempty"`
	LastError        string  `json:"last_error,omitempty"`
	// DLXDepth is the cumulative monotonic counter (Phase 1 metric).
	DLXDepth int32 `json:"dlx_depth"`
	// DLXQueueDepth is the live count of currently-parked messages in
	// dlx.audit-export (Phase 2). `-1` signals the Mgmt API is
	// unreachable so the FE can render "depth unknown" distinct from
	// "empty."
	DLXQueueDepth int32  `json:"dlx_queue_depth"`
	UpdatedAt     string `json:"updated_at"`
}

// auditExportPutBody is the PUT body. `hmac_secret` / `bearer_token`
// carry plaintext only when the operator is rotating them; the FE
// sends "" otherwise and the audit service's "leave alone" contract
// preserves the existing ciphertext. `hmac_secret_clear` /
// `bearer_token_clear` explicitly revoke the column.
type auditExportPutBody struct {
	Enabled          bool   `json:"enabled"`
	Format           string `json:"format"`
	TargetURL        string `json:"target_url"`
	HMACSecret       string `json:"hmac_secret"`
	HMACSecretClear  bool   `json:"hmac_secret_clear"`
	BearerToken      string `json:"bearer_token"`
	BearerTokenClear bool   `json:"bearer_token_clear"`
	EventFiltersJSON string `json:"event_filters_json"`
}

// auditExportTestResponse is the body returned by the Test endpoint.
// `rendered_event` echoes the exact wire payload the audit service
// shipped to the SIEM so the FE can render it as evidence under the
// "Send test event" button.
type auditExportTestResponse struct {
	Delivered     bool   `json:"delivered"`
	Error         string `json:"error,omitempty"`
	RenderedEvent string `json:"rendered_event,omitempty"`
}

// RegisterWorkspaceAuditExport mounts the routes. Called from
// Handler.Register.
func (h *Handler) RegisterWorkspaceAuditExport(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/workspace/me/audit-export", authMW(http.HandlerFunc(h.handleGetAuditExport)))
	mux.Handle("PUT /api/v1/workspace/me/audit-export", authMW(http.HandlerFunc(h.handlePutAuditExport)))
	mux.Handle("DELETE /api/v1/workspace/me/audit-export", authMW(http.HandlerFunc(h.handleDeleteAuditExport)))
	mux.Handle("POST /api/v1/workspace/me/audit-export/test", authMW(http.HandlerFunc(h.handleTestAuditExport)))
	mux.Handle("POST /api/v1/workspace/me/audit-export/drain", authMW(http.HandlerFunc(h.handleDrainAuditExport)))
}

func (h *Handler) handleGetAuditExport(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		// requireDomainAdmin is the tenant-wide admin gate shared across
		// workspace surfaces (domains, audit-export, proxy-cache, etc.).
		// Phase 5.2: now requires tenant-admin or platform-admin, not
		// merely any org-level admin (Review §A1 Top-5 #2 fix).
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	resp, err := h.audit.GetAuditExportConfig(r.Context(), &auditv1.GetAuditExportConfigRequest{TenantId: tenantID})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			// Distinct "no config yet" surface — the FE renders the
			// empty form rather than treating this as an error toast.
			writeJSON(w, http.StatusOK, map[string]any{"config": nil})
			return
		}
		slog.Error("GetAuditExportConfig", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to load audit export config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": toAuditExportResponse(resp)})
}

func (h *Handler) handlePutAuditExport(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body auditExportPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// created_by sources from the actor user_id so the audit service
	// can record who first configured streaming. Empty for principals
	// without a user_id (system-driven seeders).
	createdBy := middleware.UserIDFromContext(r.Context())

	resp, err := h.audit.PutAuditExportConfig(r.Context(), &auditv1.PutAuditExportConfigRequest{
		TenantId:         tenantID,
		Enabled:          body.Enabled,
		Format:           body.Format,
		TargetUrl:        body.TargetURL,
		HmacSecret:       body.HMACSecret,
		HmacSecretClear:  body.HMACSecretClear,
		BearerToken:      body.BearerToken,
		BearerTokenClear: body.BearerTokenClear,
		EventFiltersJson: body.EventFiltersJSON,
		CreatedBy:        createdBy,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, s.Message())
				return
			case codes.FailedPrecondition:
				// "secrets key not configured" — surface the audit
				// service's actionable error verbatim.
				writeError(w, http.StatusPreconditionFailed, s.Message())
				return
			}
		}
		slog.Error("PutAuditExportConfig", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to save audit export config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": toAuditExportResponse(resp)})
}

func (h *Handler) handleDeleteAuditExport(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	if _, err := h.audit.DeleteAuditExportConfig(r.Context(), &auditv1.DeleteAuditExportConfigRequest{TenantId: tenantID}); err != nil {
		slog.Error("DeleteAuditExportConfig", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to clear audit export config")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleDrainAuditExport(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	resp, err := h.audit.DrainAuditExportDLX(r.Context(), &auditv1.DrainAuditExportDLXRequest{TenantId: tenantID})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.Unavailable:
				writeError(w, http.StatusServiceUnavailable, "audit-export DLX probe not wired on the audit service")
				return
			}
		}
		slog.Error("DrainAuditExportDLX", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to drain audit-export DLX")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"republished": resp.GetRepublished()})
}

func (h *Handler) handleTestAuditExport(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	resp, err := h.audit.TestAuditExportConfig(r.Context(), &auditv1.TestAuditExportConfigRequest{TenantId: tenantID})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "no audit export config to test")
				return
			case codes.Unavailable:
				writeError(w, http.StatusServiceUnavailable, "audit export tester not wired on the audit service")
				return
			}
		}
		slog.Error("TestAuditExportConfig", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to send test event")
		return
	}
	writeJSON(w, http.StatusOK, auditExportTestResponse{
		Delivered:     resp.GetDelivered(),
		Error:         resp.GetError(),
		RenderedEvent: resp.GetRenderedEvent(),
	})
}

// toAuditExportResponse converts the proto message to the wire JSON.
// Timestamps formatted via the same ISO-8601 layout the rest of the
// BFF emits so the FE date hooks stay uniform.
func toAuditExportResponse(c *auditv1.AuditExportConfig) AuditExportConfigResponse {
	out := AuditExportConfigResponse{
		ID:               c.GetId(),
		Enabled:          c.GetEnabled(),
		Format:           c.GetFormat(),
		TargetURL:        c.GetTargetUrl(),
		HMACSecretSet:    c.GetHmacSecretSet(),
		BearerTokenSet:   c.GetBearerTokenSet(),
		EventFiltersJSON: c.GetEventFiltersJson(),
		LastError:        c.GetLastError(),
		DLXDepth:         c.GetDlxDepth(),
		DLXQueueDepth:    c.GetDlxQueueDepth(),
		UpdatedAt:        c.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339Nano),
	}
	if c.GetLastSuccessAt() != nil {
		s := c.GetLastSuccessAt().AsTime().UTC().Format(time.RFC3339Nano)
		out.LastSuccessAt = &s
	}
	if c.GetLastAttemptAt() != nil {
		s := c.GetLastAttemptAt().AsTime().UTC().Format(time.RFC3339Nano)
		out.LastAttemptAt = &s
	}
	return out
}

// errAuditExportUnconfigured is exported in case any test code wants
// to assert on the "no config" branch — the handler itself returns
// the empty `{"config": null}` shape rather than an error, but
// callers downstream may want to detect the boundary.
var errAuditExportUnconfigured = errors.New("audit export not configured")
