// Package handler implements the gRPC ProxyService and the OCI pull-through HTTP handler.
package handler

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	aescrypto "github.com/steveokay/oci-janus/libs/crypto/aes"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
	"github.com/steveokay/oci-janus/services/proxy/internal/upstream"
)

// GRPCHandler implements proxyv1.ProxyServiceServer.
type GRPCHandler struct {
	proxyv1.UnimplementedProxyServiceServer
	repo  *repository.Repository
	key   []byte // 32-byte AES-256 key for credential encryption
}

// NewGRPCHandler constructs a GRPCHandler.
func NewGRPCHandler(repo *repository.Repository, credentialKeyHex string) (*GRPCHandler, error) {
	key, err := hex.DecodeString(credentialKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode credential key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("credential key must be 32 bytes, got %d", len(key))
	}
	return &GRPCHandler{repo: repo, key: key}, nil
}

// RegisterUpstream creates a new upstream registry configuration for a tenant.
func (h *GRPCHandler) RegisterUpstream(ctx context.Context, req *proxyv1.RegisterUpstreamRequest) (*proxyv1.Upstream, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}
	authType := req.GetAuthType()
	if authType == "" {
		authType = "none"
	}
	if authType != "none" && authType != "basic" && authType != "token" {
		return nil, status.Errorf(codes.InvalidArgument, "auth_type must be none, basic, or token; got %q", authType)
	}

	// Validate URL is HTTPS and not a private address (SSRF guard).
	if err := upstream.ValidateUpstreamURL(req.GetUrl()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid upstream URL: %v", err)
	}

	// Encrypt password before storage.
	var passwordEnc []byte
	if req.GetPassword() != "" {
		passwordEnc, err = aescrypto.Encrypt([]byte(req.GetPassword()), h.key)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt credential: %v", err)
		}
	}

	ttl := req.GetTtlSeconds()
	if ttl <= 0 {
		ttl = 3600
	}

	rec, err := h.repo.CreateUpstream(ctx, tenantID, req.GetName(), req.GetUrl(),
		authType, req.GetUsername(), passwordEnc, ttl)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "upstream %q already exists for tenant", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "create upstream: %v", err)
	}

	return upstreamToProto(rec), nil
}

// DeleteUpstream removes an upstream registry configuration.
func (h *GRPCHandler) DeleteUpstream(ctx context.Context, req *proxyv1.DeleteUpstreamRequest) (*emptypb.Empty, error) {
	upstreamID, err := uuid.Parse(req.GetUpstreamId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid upstream_id: %v", err)
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}

	if err := h.repo.DeleteUpstream(ctx, upstreamID, tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "upstream not found")
		}
		return nil, status.Errorf(codes.Internal, "delete upstream: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListUpstreams streams all upstream registry configurations for a tenant.
func (h *GRPCHandler) ListUpstreams(req *proxyv1.ListUpstreamsRequest, stream proxyv1.ProxyService_ListUpstreamsServer) error {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}

	recs, err := h.repo.ListUpstreams(stream.Context(), tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "list upstreams: %v", err)
	}

	for _, rec := range recs {
		if err := stream.Send(upstreamToProto(rec)); err != nil {
			return err
		}
	}
	return nil
}

func upstreamToProto(rec *repository.UpstreamRecord) *proxyv1.Upstream {
	return &proxyv1.Upstream{
		UpstreamId: rec.UpstreamID.String(),
		TenantId:   rec.TenantID.String(),
		Name:       rec.Name,
		Url:        rec.URL,
		AuthType:   rec.AuthType,
		Enabled:    rec.Enabled,
		TtlSeconds: rec.TTLSeconds,
	}
}
