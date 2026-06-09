// Package handler contains the gRPC and HTTP request handlers for registry-auth.
package handler

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// GRPCHandler implements authv1.AuthServiceServer.
type GRPCHandler struct {
	authv1.UnimplementedAuthServiceServer
	svc *service.Service
}

// NewGRPCHandler creates a GRPCHandler backed by the given service.
func NewGRPCHandler(svc *service.Service) *GRPCHandler {
	return &GRPCHandler{svc: svc}
}

// ValidateToken parses the JWT, checks the revocation list, and returns the claims.
func (h *GRPCHandler) ValidateToken(ctx context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	claims, err := h.svc.ValidateToken(ctx, req.GetToken())
	if err != nil {
		if errors.Is(err, service.ErrTokenRevoked) {
			return nil, status.Error(codes.Unauthenticated, "token has been revoked")
		}
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	protoAccess := make([]*authv1.RepositoryAccess, len(claims.Access))
	for i, a := range claims.Access {
		protoAccess[i] = &authv1.RepositoryAccess{
			Type:    a.Type,
			Name:    a.Name,
			Actions: a.Actions,
		}
	}

	return &authv1.ValidateTokenResponse{
		Valid:     true,
		UserId:    claims.Subject,
		TenantId:  claims.TenantID,
		Jti:       claims.ID,
		Access:    protoAccess,
		ExpiresAt: timestamppb.New(claims.ExpiresAt.Time),
	}, nil
}

// ValidateAPIKey checks the key hash and returns the associated identity.
func (h *GRPCHandler) ValidateAPIKey(ctx context.Context, req *authv1.ValidateAPIKeyRequest) (*authv1.ValidateAPIKeyResponse, error) {
	keyID, err := uuid.Parse(req.GetKeyId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid key_id")
	}

	key, err := h.svc.ValidateAPIKey(ctx, keyID, req.GetRawSecret())
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) || errors.Is(err, service.ErrKeyExpired) {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &authv1.ValidateAPIKeyResponse{
		Valid:     true,
		UserId:    key.UserID.String(),
		TenantId:  key.TenantID.String(),
		Access:    scopesToProto(key.Scopes),
	}, nil
}

// GetUserPermissions returns the access scopes and roles for a user.
// Sprint 1: RBAC is not yet implemented; returns empty access and roles if the user exists.
func (h *GRPCHandler) GetUserPermissions(ctx context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	_, err = h.svc.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &authv1.GetUserPermissionsResponse{
		Access: []*authv1.RepositoryAccess{},
		Roles:  []string{},
	}, nil
}

// scopesToProto wraps a flat scope list as a single wildcard RepositoryAccess.
// This is a Sprint 1 simplification; full scope-to-access mapping comes later.
func scopesToProto(scopes []string) []*authv1.RepositoryAccess {
	if len(scopes) == 0 {
		return nil
	}
	return []*authv1.RepositoryAccess{{
		Type:    "repository",
		Name:    "*",
		Actions: scopes,
	}}
}
