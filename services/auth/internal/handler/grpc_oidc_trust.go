// Package handler — grpc_oidc_trust.go is the gRPC layer for the FUT-001
// federated workload identity feature. Five RPCs map to OIDCTrustService:
//
//   ListOIDCTrusts          — admin list
//   CreateOIDCTrust         — admin create
//   UpdateOIDCTrust         — admin mutate display_name / pattern / TTL
//   DeleteOIDCTrust         — admin remove
//   ExchangeWorkloadToken   — public exchange (CI runner → registry JWT)
//
// Per the pattern established by grpc_tenant_users.go, the gRPC layer
// trusts its caller — RBAC gates land in services/management's BFF.
// The exchange RPC is the only one publicly exposed; it has its own
// authorisation model (the OIDC JWT itself is the credential) so no
// pre-check is needed at the gRPC layer beyond mTLS.
package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// errOIDCNotConfigured is returned when an OIDC RPC is invoked but the
// service was constructed without an OIDCTrustService. Wraps a clear
// codes.Unimplemented so callers learn the feature is off rather than
// seeing a generic 5xx.
var errOIDCNotConfigured = status.Error(codes.Unimplemented, "OIDC trust feature is not configured (OIDC_ALLOWED_ISSUERS is empty)")

// ListOIDCTrusts returns every trust row for the tenant.
func (h *GRPCHandler) ListOIDCTrusts(ctx context.Context, req *authv1.ListOIDCTrustsRequest) (*authv1.ListOIDCTrustsResponse, error) {
	if h.oidc == nil {
		return nil, errOIDCNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	trusts, err := h.oidc.List(ctx, tenantID)
	if err != nil {
		return nil, errcodes.MapDBError(err, "list oidc trusts")
	}
	out := make([]*authv1.OIDCTrust, 0, len(trusts))
	for _, t := range trusts {
		out = append(out, oidcTrustToProto(t))
	}
	return &authv1.ListOIDCTrustsResponse{Trusts: out}, nil
}

// CreateOIDCTrust validates the input + inserts a new trust row.
func (h *GRPCHandler) CreateOIDCTrust(ctx context.Context, req *authv1.CreateOIDCTrustRequest) (*authv1.OIDCTrust, error) {
	if h.oidc == nil {
		return nil, errOIDCNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	saID, err := uuid.Parse(req.GetServiceAccountId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid service_account_id")
	}
	row, err := h.oidc.Create(ctx, service.CreateOIDCTrustInput{
		TenantID:            tenantID,
		ServiceAccountID:    saID,
		DisplayName:         req.GetDisplayName(),
		IssuerURL:           req.GetIssuerUrl(),
		Audience:            req.GetAudience(),
		SubjectPattern:      req.GetSubjectPattern(),
		JWKSCacheTTLSeconds: req.GetJwksCacheTtlSeconds(),
		// ActorID is captured by the BFF (REST) layer from the JWT
		// claims and threaded into the gRPC metadata in a future task;
		// for now the audit event records empty actor when the gRPC
		// caller doesn't set it.
	})
	if err != nil {
		// Service layer already returns clean gRPC codes; just propagate.
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, errcodes.MapDBError(err, "create oidc trust")
	}
	return oidcTrustToProto(row), nil
}

// UpdateOIDCTrust mutates display_name / subject_pattern / TTL.
func (h *GRPCHandler) UpdateOIDCTrust(ctx context.Context, req *authv1.UpdateOIDCTrustRequest) (*authv1.OIDCTrust, error) {
	if h.oidc == nil {
		return nil, errOIDCNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid id")
	}
	row, err := h.oidc.Update(ctx, service.UpdateOIDCTrustInput{
		ID:                  id,
		TenantID:            tenantID,
		DisplayName:         req.GetDisplayName(),
		SubjectPattern:      req.GetSubjectPattern(),
		JWKSCacheTTLSeconds: req.GetJwksCacheTtlSeconds(),
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, errcodes.MapDBError(err, "update oidc trust")
	}
	return oidcTrustToProto(row), nil
}

// DeleteOIDCTrust removes a trust row.
func (h *GRPCHandler) DeleteOIDCTrust(ctx context.Context, req *authv1.DeleteOIDCTrustRequest) (*emptypb.Empty, error) {
	if h.oidc == nil {
		return nil, errOIDCNotConfigured
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid id")
	}
	if err := h.oidc.Delete(ctx, tenantID, id, ""); err != nil {
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, errcodes.MapDBError(err, "delete oidc trust")
	}
	return &emptypb.Empty{}, nil
}

// ExchangeWorkloadToken is the public exchange path. Validates the OIDC
// JWT and returns a registry JWT scoped to the matching trust's SA.
// Most callers reach this via the HTTP endpoint (POST /auth/token/workload)
// rather than the gRPC RPC, but the gRPC surface exists for internal
// callers that prefer gRPC + mTLS.
func (h *GRPCHandler) ExchangeWorkloadToken(ctx context.Context, req *authv1.ExchangeWorkloadTokenRequest) (*authv1.ExchangeWorkloadTokenResponse, error) {
	if h.oidc == nil {
		return nil, errOIDCNotConfigured
	}
	res, err := h.oidc.ExchangeWorkloadToken(ctx, req.GetOidcJwt())
	if err != nil {
		// Service layer already returns clean gRPC codes (Unauthenticated
		// / Unavailable). Just propagate.
		if s, ok := status.FromError(err); ok {
			return nil, s.Err()
		}
		return nil, status.Error(codes.Internal, "exchange failed")
	}
	return &authv1.ExchangeWorkloadTokenResponse{
		AccessToken: res.AccessToken,
		ExpiresIn:   res.ExpiresIn,
		TokenType:   res.TokenType,
	}, nil
}

// oidcTrustToProto converts a repository row to its proto representation.
// last_used_at is nullable on the row but the proto field is always
// present — we emit a nil Timestamp when last_used_at is NULL so the
// FE can render "never used" without parsing a zero-value sentinel.
func oidcTrustToProto(t *repository.OIDCTrust) *authv1.OIDCTrust {
	var lastUsed *timestamppb.Timestamp
	if t.LastUsedAt != nil {
		lastUsed = timestamppb.New(*t.LastUsedAt)
	}
	return &authv1.OIDCTrust{
		Id:                  t.ID.String(),
		TenantId:            t.TenantID.String(),
		ServiceAccountId:    t.ServiceAccountID.String(),
		DisplayName:         t.DisplayName,
		IssuerUrl:           t.IssuerURL,
		Audience:            t.Audience,
		SubjectPattern:      t.SubjectPattern,
		JwksCacheTtlSeconds: t.JWKSCacheTTLSeconds,
		CreatedAt:           timestamppb.New(t.CreatedAt),
		UpdatedAt:           timestamppb.New(t.UpdatedAt),
		LastUsedAt:          lastUsed,
	}
}

