// Package handler — webhook management endpoints (FE-API-021..024).
//
// These handlers translate REST calls into gRPC calls against
// registry-webhook. Webhook resources are tenant-scoped, so authorization
// requires that the caller hold at least admin role on some org within
// the active tenant. The platform-admin marker (`org=*` admin grant) also
// satisfies this check.
//
// Secrets are generated server-side (32 bytes from crypto/rand) and returned
// to the caller exactly once — on POST create and on POST rotate-secret.
// They are never persisted in plaintext; registry-webhook encrypts with
// AES-256-GCM before writing the row.
package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// EndpointResponse is the JSON wire form of a webhook endpoint.
// `secret` is included only on the create + rotate-secret responses; for
// list / get / update it stays empty.
type EndpointResponse struct {
	EndpointID string    `json:"endpoint_id"`
	URL        string    `json:"url"`
	Events     []string  `json:"events"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	// Secret is the freshly generated HMAC key. Returned exactly once —
	// the API has no way to recover it later. Omitted on every read path.
	Secret string `json:"secret,omitempty"`
}

// DeliveryResponse is the JSON wire form of one dispatch attempt.
type DeliveryResponse struct {
	DeliveryID    string     `json:"delivery_id"`
	EndpointID    string     `json:"endpoint_id"`
	EventType     string     `json:"event_type"`
	Status        string     `json:"status"`
	Attempts      int32      `json:"attempts"`
	MaxAttempts   int32      `json:"max_attempts"`
	LastError     string     `json:"last_error,omitempty"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	CreatedAt     time.Time  `json:"created_at"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
}

// createWebhookBody is the JSON body for POST /api/v1/webhooks.
type createWebhookBody struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// updateWebhookBody is the JSON body for PATCH /api/v1/webhooks/{id}.
// All three fields are pointer-typed so the handler can tell "leave alone"
// from "set to zero value". `events` uses a non-pointer slice and treats
// empty as "leave alone" — same convention as the gRPC layer.
type updateWebhookBody struct {
	URL    *string  `json:"url,omitempty"`
	Events []string `json:"events,omitempty"`
	Active *bool    `json:"active,omitempty"`
}

// requireWebhookAdmin gates webhook mutations and reads.
//
// Webhooks are tenant-wide resources — a single endpoint subscription fires
// for events across ALL orgs in the tenant. An org-A admin must NOT be able
// to configure webhooks that receive org-B's push events or exfiltrate
// org-B's data via the delivery URL (Review §A1, Top-5 #2 fix).
//
// Valid callers:
//   - Platform-admin marker (admin, org, "*")
//   - Tenant-scoped admin (admin, tenant, <tenant_id>)
//
// REDESIGN-001 Phase 5.4 / Decision #24: service-account principals are
// denied. A webhook delivery URL is an exfiltration channel — letting an
// API key configure one because its owner happens to be a tenant admin
// would be a credential-grade privilege escalation.
func (h *Handler) requireWebhookAdmin(r *http.Request) bool {
	if middleware.PrincipalKindFromContext(r.Context()) == middleware.PrincipalKindServiceAccount {
		return false
	}
	// Phase 5.1 tail (2026-06-29): global admins bypass — see
	// handler.go:requireDomainAdmin for the full rationale.
	if h.effectiveGlobalAdmin(r) {
		return true
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	return effectiveTenantAdmin(h.getUserAssignments(r), tenantID)
}

// RegisterWebhooks mounts the FE-API-021..024 webhook routes onto mux under
// the authMW middleware. Wired from Handler.Register.
func (h *Handler) RegisterWebhooks(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/webhooks", authMW(http.HandlerFunc(h.handleListWebhooks)))
	mux.Handle("POST /api/v1/webhooks", authMW(http.HandlerFunc(h.handleCreateWebhook)))
	mux.Handle("PATCH /api/v1/webhooks/{id}", authMW(http.HandlerFunc(h.handleUpdateWebhook)))
	mux.Handle("DELETE /api/v1/webhooks/{id}", authMW(http.HandlerFunc(h.handleDeleteWebhook)))
	mux.Handle("GET /api/v1/webhooks/{id}/deliveries", authMW(http.HandlerFunc(h.handleListWebhookDeliveries)))
	mux.Handle("GET /api/v1/webhooks/{id}/deliveries/{delivery_id}", authMW(http.HandlerFunc(h.handleGetDelivery)))
	mux.Handle("POST /api/v1/webhooks/{id}/test", authMW(http.HandlerFunc(h.handleTestWebhook)))
	mux.Handle("POST /api/v1/webhooks/{id}/rotate-secret", authMW(http.HandlerFunc(h.handleRotateWebhookSecret)))
}

// ---------------------------------------------------------------------------
// GET /api/v1/webhooks
// ---------------------------------------------------------------------------

func (h *Handler) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	// PENTEST-027: list responses include the full webhook URL, which
	// operators commonly use to carry a per-endpoint auth token (query
	// param or userinfo). Gate the read behind the same admin marker as
	// mutations so a low-privilege tenant reader can't exfiltrate another
	// team's webhook secret. Same gate for /deliveries below.
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	stream, err := h.webhook.ListEndpoints(r.Context(), &webhookv1.ListEndpointsRequest{TenantId: tenantID})
	if err != nil {
		slog.Error("ListEndpoints", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list webhooks")
		return
	}

	endpoints := []EndpointResponse{}
	for {
		ep, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			slog.Error("ListEndpoints stream", "err", recvErr)
			break
		}
		endpoints = append(endpoints, endpointToResponse(ep, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": endpoints})
}

// ---------------------------------------------------------------------------
// POST /api/v1/webhooks
// ---------------------------------------------------------------------------

func (h *Handler) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body createWebhookBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" || len(body.Events) == 0 {
		writeError(w, http.StatusBadRequest, "url and events are required")
		return
	}

	secret, err := generateWebhookSecret()
	if err != nil {
		slog.Error("generate webhook secret", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to generate secret")
		return
	}

	ep, err := h.webhook.CreateEndpoint(r.Context(), &webhookv1.CreateEndpointRequest{
		TenantId: tenantID,
		Url:      body.URL,
		Events:   body.Events,
		Secret:   secret,
	})
	if err != nil {
		mapWebhookGRPCError(w, err, "create webhook")
		return
	}
	writeJSON(w, http.StatusCreated, endpointToResponse(ep, secret))
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/webhooks/{id}
// ---------------------------------------------------------------------------

func (h *Handler) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body updateWebhookBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req := &webhookv1.UpdateEndpointRequest{
		EndpointId: id,
		TenantId:   tenantID,
		Url:        body.URL,
		Events:     body.Events,
		Active:     body.Active,
	}
	ep, err := h.webhook.UpdateEndpoint(r.Context(), req)
	if err != nil {
		mapWebhookGRPCError(w, err, "update webhook")
		return
	}
	writeJSON(w, http.StatusOK, endpointToResponse(ep, ""))
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/webhooks/{id}
// ---------------------------------------------------------------------------

func (h *Handler) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	if _, err := h.webhook.DeleteEndpoint(r.Context(), &webhookv1.DeleteEndpointRequest{
		EndpointId: id,
		TenantId:   tenantID,
	}); err != nil {
		mapWebhookGRPCError(w, err, "delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/v1/webhooks/{id}/deliveries
// ---------------------------------------------------------------------------

func (h *Handler) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	// PENTEST-027: delivery rows carry `last_error` strings produced by the
	// dispatcher; even with the URL sanitised inside DeliverWithResult, the
	// status / attempts / endpoint_id triple is operational data that
	// shouldn't go to anyone below admin.
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	limit := int32(50)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
			limit = int32(n) //nolint:gosec // bounded above
		}
	}

	req := &webhookv1.ListDeliveriesRequest{
		EndpointId: id,
		TenantId:   tenantID,
		Limit:      limit,
	}
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		req.Since = timestamppb.New(t)
	}

	stream, err := h.webhook.ListDeliveries(r.Context(), req)
	if err != nil {
		slog.Error("ListDeliveries", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list deliveries")
		return
	}
	deliveries := []DeliveryResponse{}
	for {
		d, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			slog.Error("ListDeliveries stream", "err", recvErr)
			break
		}
		deliveries = append(deliveries, deliveryToResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

// ---------------------------------------------------------------------------
// POST /api/v1/webhooks/{id}/test
// ---------------------------------------------------------------------------

// testDispatchResponse mirrors the proto so we don't leak gRPC tag names.
type testDispatchResponse struct {
	StatusCode int32  `json:"status_code"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func (h *Handler) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.webhook.TestDispatch(r.Context(), &webhookv1.TestDispatchRequest{
		EndpointId: id,
		TenantId:   tenantID,
	})
	if err != nil {
		mapWebhookGRPCError(w, err, "test dispatch")
		return
	}
	writeJSON(w, http.StatusOK, testDispatchResponse{
		StatusCode: resp.GetStatusCode(),
		DurationMs: resp.GetDurationMs(),
		Error:      resp.GetError(),
	})
}

// ---------------------------------------------------------------------------
// POST /api/v1/webhooks/{id}/rotate-secret
// ---------------------------------------------------------------------------

// rotateSecretResponse exposes the freshly generated plaintext secret. Same
// pattern as create — returned exactly once, no recovery path.
type rotateSecretResponse struct {
	Secret string `json:"secret"`
}

func (h *Handler) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if h.webhook == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireWebhookAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	secret, err := generateWebhookSecret()
	if err != nil {
		slog.Error("generate webhook secret", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to generate secret")
		return
	}
	if _, err := h.webhook.RotateEndpointSecret(r.Context(), &webhookv1.RotateEndpointSecretRequest{
		EndpointId: id,
		TenantId:   tenantID,
		Secret:     secret,
	}); err != nil {
		mapWebhookGRPCError(w, err, "rotate webhook secret")
		return
	}
	writeJSON(w, http.StatusOK, rotateSecretResponse{Secret: secret})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// generateWebhookSecret returns a 32-byte cryptographically random HMAC key
// as 64-char hex. Same length and encoding the existing dev seed uses, so
// rotating an old key is a one-for-one swap.
func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// endpointToResponse converts a proto Endpoint to its JSON wire form,
// optionally attaching a one-time plaintext secret (for create / rotate).
func endpointToResponse(ep *webhookv1.Endpoint, secret string) EndpointResponse {
	return EndpointResponse{
		EndpointID: ep.GetEndpointId(),
		URL:        ep.GetUrl(),
		Events:     ep.GetEvents(),
		Active:     ep.GetActive(),
		CreatedAt:  ep.GetCreatedAt().AsTime(),
		Secret:     secret,
	}
}

// deliveryToResponse converts a proto Delivery to its JSON wire form. A nil
// `delivered_at` keeps the field out of the JSON entirely — `omitempty`
// drops the key when the pointer is nil.
func deliveryToResponse(d *webhookv1.Delivery) DeliveryResponse {
	out := DeliveryResponse{
		DeliveryID:    d.GetDeliveryId(),
		EndpointID:    d.GetEndpointId(),
		EventType:     d.GetEventType(),
		Status:        d.GetStatus(),
		Attempts:      d.GetAttempts(),
		MaxAttempts:   d.GetMaxAttempts(),
		LastError:     d.GetLastError(),
		NextAttemptAt: d.GetNextAttemptAt().AsTime(),
		CreatedAt:     d.GetCreatedAt().AsTime(),
	}
	if d.GetDeliveredAt() != nil {
		t := d.GetDeliveredAt().AsTime()
		out.DeliveredAt = &t
	}
	return out
}

// mapWebhookGRPCError translates the gRPC status codes the webhook service
// returns into HTTP responses. Generic error strings — internal gRPC detail
// is intentionally not leaked to the API client.
func mapWebhookGRPCError(w http.ResponseWriter, err error, opLabel string) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.InvalidArgument:
		// Log the real gRPC message server-side (may contain SSRF guard
		// internals such as blocked IP addresses) but return a generic string
		// to the caller so that probe attacks cannot enumerate private ranges.
		slog.Warn(opLabel+" invalid argument", "detail", st.Message())
		writeError(w, http.StatusBadRequest, "invalid request")
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "endpoint not found")
	case codes.PermissionDenied:
		writeError(w, http.StatusForbidden, "permission denied")
	default:
		slog.Error(opLabel, "err", err)
		writeError(w, http.StatusInternalServerError, opLabel+" failed")
	}
}
