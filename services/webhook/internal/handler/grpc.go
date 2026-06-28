// Package handler implements the WebhookService gRPC server.
package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	libsaes "github.com/steveokay/oci-janus/libs/crypto/aes"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/webhook/internal/delivery"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
)

// webhookRepo is the database interface used by the handler.
// Extracted as an interface so unit tests can substitute a fake without a real
// PostgreSQL connection.
type webhookRepo interface {
	CreateEndpoint(ctx context.Context, tenantID uuid.UUID, url string, events []string, secretEnc string) (*repository.EndpointRecord, error)
	DeleteEndpoint(ctx context.Context, endpointID, tenantID uuid.UUID) error
	ListEndpoints(ctx context.Context, tenantID uuid.UUID) ([]*repository.EndpointRecord, error)
	GetEndpointForTenant(ctx context.Context, endpointID, tenantID uuid.UUID) (*repository.EndpointRecord, error)
	UpdateEndpoint(ctx context.Context, endpointID, tenantID uuid.UUID, url *string, events []string, active *bool) (*repository.EndpointRecord, error)
	RotateSecret(ctx context.Context, endpointID, tenantID uuid.UUID, secretEnc string) error
	ListDeliveries(ctx context.Context, endpointID, tenantID uuid.UUID, since time.Time, limit int) ([]*repository.DeliveryRecord, error)
	GetDelivery(ctx context.Context, endpointID, deliveryID, tenantID uuid.UUID) (*repository.DeliveryRecord, error)
}

// testDispatcher is the subset of *delivery.Dispatcher used by TestDispatch.
// Defined as an interface so unit tests can substitute a hand-written fake
// without making real HTTP calls.
type testDispatcher interface {
	DeliverWithResult(ctx context.Context, targetURL string, payload []byte, hmacKey []byte) (int, int64, error)
}

// GRPCHandler implements webhookv1.WebhookServiceServer.
type GRPCHandler struct {
	webhookv1.UnimplementedWebhookServiceServer
	repo          webhookRepo
	dispatcher    testDispatcher
	credentialKey []byte
}

// New creates a GRPCHandler with the given repository and credential key.
// dispatcher is the same instance the worker uses; TestDispatch shares it so
// the test path applies the same SSRF guard + timeouts as production sends.
func New(repo *repository.Repository, dispatcher *delivery.Dispatcher, credentialKeyHex string) (*GRPCHandler, error) {
	return newWithRepo(repo, dispatcher, credentialKeyHex)
}

// newWithRepo is the internal constructor used by both New and tests.
func newWithRepo(repo webhookRepo, dispatcher testDispatcher, credentialKeyHex string) (*GRPCHandler, error) {
	key, err := hex.DecodeString(credentialKeyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("CREDENTIAL_KEY_HEX must be a 64-character hex string (32 bytes)")
	}
	return &GRPCHandler{repo: repo, dispatcher: dispatcher, credentialKey: key}, nil
}

// CreateEndpoint registers a new webhook endpoint for a tenant.
// The secret is encrypted with AES-256-GCM before being stored — it is never
// returned after this response.
func (h *GRPCHandler) CreateEndpoint(ctx context.Context, req *webhookv1.CreateEndpointRequest) (*webhookv1.Endpoint, error) {
	if req.TenantId == "" || req.Url == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and url are required")
	}
	if len(req.Events) == 0 {
		return nil, status.Error(codes.InvalidArgument, "events must not be empty")
	}

	if err := delivery.ValidateURL(req.Url); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid webhook URL: %v", err)
	}

	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Encrypt the HMAC secret before persistence — never stored or returned in plaintext.
	ct, err := libsaes.Encrypt([]byte(req.Secret), h.credentialKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	secretEnc := hex.EncodeToString(ct)

	rec, err := h.repo.CreateEndpoint(ctx, tenantID, req.Url, req.Events, secretEnc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create endpoint: %v", err)
	}

	return endpointToProto(rec), nil
}

// DeleteEndpoint removes a webhook endpoint.
func (h *GRPCHandler) DeleteEndpoint(ctx context.Context, req *webhookv1.DeleteEndpointRequest) (*emptypb.Empty, error) {
	if req.EndpointId == "" || req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "endpoint_id and tenant_id are required")
	}

	endpointID, err := uuid.Parse(req.EndpointId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid endpoint_id")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	if err := h.repo.DeleteEndpoint(ctx, endpointID, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete endpoint: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListEndpoints streams all endpoints registered for a tenant.
func (h *GRPCHandler) ListEndpoints(req *webhookv1.ListEndpointsRequest, stream webhookv1.WebhookService_ListEndpointsServer) error {
	if req.TenantId == "" {
		return status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	endpoints, err := h.repo.ListEndpoints(stream.Context(), tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "list endpoints: %v", err)
	}

	for _, ep := range endpoints {
		if err := stream.Send(endpointToProto(ep)); err != nil {
			return err
		}
	}
	return nil
}

func endpointToProto(rec *repository.EndpointRecord) *webhookv1.Endpoint {
	return &webhookv1.Endpoint{
		EndpointId: rec.ID.String(),
		TenantId:   rec.TenantID.String(),
		Url:        rec.URL,
		Events:     rec.Events,
		Active:     rec.Active,
		CreatedAt:  timestamppb.New(rec.CreatedAt),
		// secret is intentionally omitted — never returned after creation
	}
}

// UpdateEndpoint patches a subset of fields on an existing endpoint.
// Fields the caller didn't set (proto3 `optional` not present, or empty
// `events`) are passed as nil/empty to the repository, which leaves them
// untouched.
func (h *GRPCHandler) UpdateEndpoint(ctx context.Context, req *webhookv1.UpdateEndpointRequest) (*webhookv1.Endpoint, error) {
	if req.EndpointId == "" || req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "endpoint_id and tenant_id are required")
	}
	endpointID, err := uuid.Parse(req.EndpointId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid endpoint_id")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	var urlPtr *string
	if req.Url != nil {
		newURL := req.GetUrl()
		if err := delivery.ValidateURL(newURL); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid webhook URL: %v", err)
		}
		urlPtr = &newURL
	} else {
		// PENTEST-032: even when the caller only updates events/active, re-validate
		// the currently stored URL. A stored URL may have become RFC1918-routable
		// via DNS reassignment or IP churn since the endpoint was created.
		// The runtime dialer already blocks at send time, but opportunistic
		// re-validation removes stale bad URLs proactively.
		existing, err := h.repo.GetEndpointForTenant(ctx, endpointID, tenantID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, status.Error(codes.NotFound, "endpoint not found")
			}
			return nil, status.Errorf(codes.Internal, "get endpoint: %v", err)
		}
		if err := delivery.ValidateURL(existing.URL); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "stored webhook URL is no longer valid: %v", err)
		}
	}
	var activePtr *bool
	if req.Active != nil {
		a := req.GetActive()
		activePtr = &a
	}

	rec, err := h.repo.UpdateEndpoint(ctx, endpointID, tenantID, urlPtr, req.Events, activePtr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "endpoint not found")
		}
		return nil, status.Errorf(codes.Internal, "update endpoint: %v", err)
	}
	return endpointToProto(rec), nil
}

// RotateEndpointSecret replaces the stored HMAC ciphertext with a freshly
// encrypted version of the provided plaintext. The plaintext is never logged.
func (h *GRPCHandler) RotateEndpointSecret(ctx context.Context, req *webhookv1.RotateEndpointSecretRequest) (*emptypb.Empty, error) {
	if req.EndpointId == "" || req.TenantId == "" || req.Secret == "" {
		return nil, status.Error(codes.InvalidArgument, "endpoint_id, tenant_id, and secret are required")
	}
	endpointID, err := uuid.Parse(req.EndpointId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid endpoint_id")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	ct, err := libsaes.Encrypt([]byte(req.Secret), h.credentialKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	if err := h.repo.RotateSecret(ctx, endpointID, tenantID, hex.EncodeToString(ct)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "endpoint not found")
		}
		return nil, status.Errorf(codes.Internal, "rotate secret: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListDeliveries streams recent dispatch attempts for one endpoint.
// The dispatch payload is intentionally omitted from the wire — see the proto
// comment on `Delivery` for why.
func (h *GRPCHandler) ListDeliveries(req *webhookv1.ListDeliveriesRequest, stream webhookv1.WebhookService_ListDeliveriesServer) error {
	if req.EndpointId == "" || req.TenantId == "" {
		return status.Error(codes.InvalidArgument, "endpoint_id and tenant_id are required")
	}
	endpointID, err := uuid.Parse(req.EndpointId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid endpoint_id")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Cap the page so a forgotten `limit` doesn't drain the table.
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	var since time.Time
	if req.Since != nil {
		since = req.GetSince().AsTime()
	}

	deliveries, err := h.repo.ListDeliveries(stream.Context(), endpointID, tenantID, since, limit)
	if err != nil {
		return status.Errorf(codes.Internal, "list deliveries: %v", err)
	}

	for _, d := range deliveries {
		if err := stream.Send(deliveryToProto(d)); err != nil {
			return err
		}
	}
	return nil
}

// deliveryToProto maps the DB row to its wire form. The encrypted HMAC and
// the raw payload are intentionally not exposed.
func deliveryToProto(rec *repository.DeliveryRecord) *webhookv1.Delivery {
	d := &webhookv1.Delivery{
		DeliveryId:    rec.ID.String(),
		EndpointId:    rec.EndpointID.String(),
		TenantId:      rec.TenantID.String(),
		EventType:     rec.EventType,
		Status:        rec.Status,
		Attempts:      int32(rec.Attempts),
		MaxAttempts:   int32(rec.MaxAttempts),
		LastError:     rec.LastError,
		NextAttemptAt: timestamppb.New(rec.NextAttemptAt),
		CreatedAt:     timestamppb.New(rec.CreatedAt),
	}
	if !rec.DeliveredAt.IsZero() {
		d.DeliveredAt = timestamppb.New(rec.DeliveredAt)
	}
	return d
}

// TestDispatch sends a synthetic event to the endpoint and surfaces the HTTP
// response synchronously. The send is NOT recorded in webhook_deliveries —
// it would otherwise pollute the operator's delivery log every time they
// hit "send test". The SSRF guard in the shared dispatcher still applies.
func (h *GRPCHandler) TestDispatch(ctx context.Context, req *webhookv1.TestDispatchRequest) (*webhookv1.TestDispatchResponse, error) {
	if req.EndpointId == "" || req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "endpoint_id and tenant_id are required")
	}
	endpointID, err := uuid.Parse(req.EndpointId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid endpoint_id")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	ep, err := h.repo.GetEndpointForTenant(ctx, endpointID, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "endpoint not found")
		}
		return nil, status.Errorf(codes.Internal, "get endpoint: %v", err)
	}

	hmacKey, err := decryptSecret(ep.SecretEnc, h.credentialKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt secret: %v", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"id":          uuid.New().String(),
		"type":        "webhook.test",
		"tenant_id":   req.TenantId,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
		"version":     "1.0",
		"payload":     map[string]string{"message": "test dispatch from registry-webhook"},
	})

	// Bound the synchronous test send so a dead endpoint can't hold the gRPC
	// call open for the full client timeout.
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	code, durMs, dispatchErr := h.dispatcher.DeliverWithResult(sendCtx, ep.URL, payload, hmacKey)
	resp := &webhookv1.TestDispatchResponse{
		StatusCode: int32(code),
		DurationMs: durMs,
	}
	if dispatchErr != nil {
		resp.Error = dispatchErr.Error()
	}
	return resp, nil
}

// decryptSecret reverses the AES-256-GCM hex-encoded ciphertext stored in
// `webhook_endpoints.secret_enc`. Pulled out for symmetry with the worker's
// in-line decode and to keep TestDispatch readable.
func decryptSecret(hexCT string, key []byte) ([]byte, error) {
	ct, err := hex.DecodeString(hexCT)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	pt, err := libsaes.Decrypt(ct, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}

// GetDelivery (FE-API-035) returns one delivery row plus the JSON payload,
// signature header, and response body. The list-deliveries stream
// deliberately omits the payload to keep responses bounded; this single-row
// variant is the debugging companion behind an explicit click.
//
// Tenant + endpoint scoping is enforced in repository.GetDelivery — a row
// belonging to another tenant returns pgx.ErrNoRows, which we surface as
// gRPC NotFound. We never echo back the queried delivery_id with a
// "wrong tenant" error because that leaks existence.
func (h *GRPCHandler) GetDelivery(ctx context.Context, req *webhookv1.GetDeliveryRequest) (*webhookv1.DeliveryDetail, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	endpointID, err := uuid.Parse(req.GetEndpointId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "endpoint_id must be a UUID")
	}
	deliveryID, err := uuid.Parse(req.GetDeliveryId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "delivery_id must be a UUID")
	}

	rec, err := h.repo.GetDelivery(ctx, endpointID, deliveryID, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "delivery not found")
		}
		return nil, status.Error(codes.Internal, "failed to load delivery")
	}

	return &webhookv1.DeliveryDetail{
		Delivery:        deliveryToProto(rec),
		PayloadJson:     string(rec.Payload),
		SignatureHeader: "", // not currently stored; surface as empty
		ResponseBody:    "", // not currently stored; surface as empty
	}, nil
}
