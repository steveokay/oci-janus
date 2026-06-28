// Package handler — webhook_delivery_detail.go
//
// FE-API-035 — GET /api/v1/webhooks/{id}/deliveries/{delivery_id}
//
// Companion to FE-API-022's list-deliveries endpoint. The list response
// deliberately omits the JSON payload to stay bounded under load; this
// single-row variant returns the full delivery for the dashboard's
// expand-row affordance and operator debugging.
//
// Auth: same `requireWebhookAdmin` gate as the list route — visibility
// into raw webhook payloads is admin-tier (operator) territory, not a
// read-only tenant member action.
//
// 404 is returned both for genuinely missing deliveries AND for
// cross-tenant guesses — the underlying GetDelivery RPC scopes by
// tenant_id, so a leaked delivery_id from another tenant cannot
// surface here. We don't distinguish the two cases in the response.
//
// `signature_header` and `response_body` are currently empty because
// the webhook_deliveries schema doesn't yet capture them; the proto
// wire shape includes the fields so a follow-up migration + dispatcher
// patch can fill them in without changing this surface.
//
// Lives in its own file so concurrent edits to handler.go don't
// conflict with the delivery-detail surface.
package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// deliveryDetailResponse is the JSON body of GET …/deliveries/{id}.
type deliveryDetailResponse struct {
	DeliveryID      string    `json:"delivery_id"`
	EndpointID      string    `json:"endpoint_id"`
	EventType       string    `json:"event_type"`
	Status          string    `json:"status"`
	Attempts        int32     `json:"attempts"`
	MaxAttempts     int32     `json:"max_attempts"`
	LastError       string    `json:"last_error,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	DeliveredAt     time.Time `json:"delivered_at,omitempty"`
	PayloadJSON     string    `json:"payload_json"`
	SignatureHeader string    `json:"signature_header"`
	ResponseBody    string    `json:"response_body"`
}

func (h *Handler) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	endpointIDStr, deliveryIDStr := r.PathValue("id"), r.PathValue("delivery_id")

	if _, err := uuid.Parse(endpointIDStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	if _, err := uuid.Parse(deliveryIDStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid delivery id")
		return
	}

	// Auth gate mirrors the list route. PENTEST-027 made list+deliveries
	// webhook-admin; the single-row variant is at least as sensitive.
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	resp, err := h.webhook.GetDelivery(r.Context(), &webhookv1.GetDeliveryRequest{
		EndpointId: endpointIDStr,
		DeliveryId: deliveryIDStr,
		TenantId:   tenantID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		switch st.Code() {
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "delivery not found")
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, "invalid arguments")
		default:
			slog.ErrorContext(r.Context(), "GetDelivery", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load delivery")
		}
		return
	}

	d := resp.GetDelivery()
	if d == nil {
		// Defensive — proto-side gRPC should never return success + nil.
		writeError(w, http.StatusInternalServerError, "empty delivery payload")
		return
	}
	out := deliveryDetailResponse{
		DeliveryID:      d.GetDeliveryId(),
		EndpointID:      d.GetEndpointId(),
		EventType:       d.GetEventType(),
		Status:          d.GetStatus(),
		Attempts:        d.GetAttempts(),
		MaxAttempts:     d.GetMaxAttempts(),
		LastError:       d.GetLastError(),
		CreatedAt:       d.GetCreatedAt().AsTime(),
		PayloadJSON:     resp.GetPayloadJson(),
		SignatureHeader: resp.GetSignatureHeader(),
		ResponseBody:    resp.GetResponseBody(),
	}
	if t := d.GetNextAttemptAt(); t != nil {
		out.NextAttemptAt = t.AsTime()
	}
	if t := d.GetDeliveredAt(); t != nil {
		out.DeliveredAt = t.AsTime()
	}

	writeJSON(w, http.StatusOK, out)
}
