// Package handler implements the WebhookService gRPC server.
package handler

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	libsaes "github.com/steveokay/oci-janus/libs/crypto/aes"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/webhook/internal/delivery"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
)

// GRPCHandler implements webhookv1.WebhookServiceServer.
type GRPCHandler struct {
	webhookv1.UnimplementedWebhookServiceServer
	repo          *repository.Repository
	credentialKey []byte
}

// New creates a GRPCHandler with the given repository and credential key.
func New(repo *repository.Repository, credentialKeyHex string) (*GRPCHandler, error) {
	key, err := hex.DecodeString(credentialKeyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("CREDENTIAL_KEY_HEX must be a 64-character hex string (32 bytes)")
	}
	return &GRPCHandler{repo: repo, credentialKey: key}, nil
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
