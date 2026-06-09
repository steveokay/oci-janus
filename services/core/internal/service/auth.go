// Package service contains the business logic for registry-core.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// TokenClaims holds the decoded claims from a validated JWT or API key.
type TokenClaims struct {
	UserID   string
	TenantID string
	Access   []*authv1.RepositoryAccess
}

// AuthClient validates tokens against registry-auth gRPC with Redis caching.
type AuthClient struct {
	grpc  authv1.AuthServiceClient
	redis *redis.Client
}

// NewAuthClient constructs an AuthClient.
func NewAuthClient(conn *grpc.ClientConn, rdb *redis.Client) *AuthClient {
	return &AuthClient{grpc: authv1.NewAuthServiceClient(conn), redis: rdb}
}

// ValidateBearer validates a Bearer JWT. Results are cached in Redis by JTI until
// the token's own expiry so we don't hit registry-auth on every request.
func (a *AuthClient) ValidateBearer(ctx context.Context, token string) (*TokenClaims, error) {
	cacheKey := "jwt:valid:" + token
	if cached, err := a.redis.Get(ctx, cacheKey).Result(); err == nil {
		// cached value is "userID:tenantID" — fast path
		parts := strings.SplitN(cached, ":", 2)
		if len(parts) == 2 {
			return &TokenClaims{UserID: parts[0], TenantID: parts[1]}, nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.grpc.ValidateToken(ctx, &authv1.ValidateTokenRequest{Token: token})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("validate token rpc: %w", err)
	}
	if !resp.GetValid() {
		return nil, ErrUnauthorized
	}

	claims := &TokenClaims{
		UserID:   resp.GetUserId(),
		TenantID: resp.GetTenantId(),
		Access:   resp.GetAccess(),
	}

	// cache until token expiry
	if exp := resp.GetExpiresAt(); exp != nil {
		ttl := time.Until(exp.AsTime())
		if ttl > 0 {
			_ = a.redis.Set(ctx, cacheKey, claims.UserID+":"+claims.TenantID, ttl).Err()
		}
	}

	return claims, nil
}

// ValidateAPIKey validates an API key credential (keyID:secret).
func (a *AuthClient) ValidateAPIKey(ctx context.Context, keyID, secret string) (*TokenClaims, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.grpc.ValidateAPIKey(ctx, &authv1.ValidateAPIKeyRequest{
		KeyId:     keyID,
		RawSecret: secret,
	})
	if err != nil {
		return nil, fmt.Errorf("validate api key rpc: %w", err)
	}
	if !resp.GetValid() {
		return nil, ErrUnauthorized
	}
	return &TokenClaims{
		UserID:   resp.GetUserId(),
		TenantID: resp.GetTenantId(),
	}, nil
}

// HasAction returns true if the claims grant the requested action on the given repository name.
func (c *TokenClaims) HasAction(repoName, action string) bool {
	for _, a := range c.Access {
		if a.GetName() == repoName || a.GetName() == "*" {
			for _, act := range a.GetActions() {
				if act == action || act == "*" {
					return true
				}
			}
		}
	}
	return false
}
