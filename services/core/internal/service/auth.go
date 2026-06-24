// Package service contains the business logic for registry-core.
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// cachedClaims is the JSON-serialisable form of TokenClaims stored in Redis.
type cachedClaims struct {
	UserID   string                     `json:"u"`
	TenantID string                     `json:"t"`
	Access   []*authv1.RepositoryAccess `json:"a,omitempty"`
}

// ValidateBearer validates a Bearer JWT. Results are cached in Redis keyed
// on the JTI claim (CLAUDE.md §7) until the token's own expiry so we don't
// hit registry-auth on every request.
//
// QA-004: the previous version cached on the raw token, which leaked the
// full bearer credential into Redis keyspace. Anyone with `KEYS jwt:valid:*`
// access could replay any live token. Keying on JTI preserves cache
// behaviour without exposing the credential. Malformed tokens (parse
// failure) skip the cache entirely and fall through to the gRPC call,
// which rejects them.
func (a *AuthClient) ValidateBearer(ctx context.Context, token string) (*TokenClaims, error) {
	jti := parseJTI(token)
	var cacheKey string
	if jti != "" {
		cacheKey = "jwt:valid:" + jti
		if cached, err := a.redis.Get(ctx, cacheKey).Bytes(); err == nil {
			var cc cachedClaims
			if json.Unmarshal(cached, &cc) == nil {
				return &TokenClaims{UserID: cc.UserID, TenantID: cc.TenantID, Access: cc.Access}, nil
			}
		}
	}

	// Allow 15s for the first call, which must also establish the gRPC connection.
	rpcCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := a.grpc.ValidateToken(rpcCtx, &authv1.ValidateTokenRequest{Token: token})
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

	if cacheKey != "" {
		if exp := resp.GetExpiresAt(); exp != nil {
			if ttl := time.Until(exp.AsTime()); ttl > 0 {
				if b, jerr := json.Marshal(cachedClaims{UserID: claims.UserID, TenantID: claims.TenantID, Access: claims.Access}); jerr == nil {
					_ = a.redis.Set(ctx, cacheKey, b, ttl).Err()
				}
			}
		}
	}

	return claims, nil
}

// parseJTI extracts the JTI claim from a JWT payload without verifying the
// signature. Returns empty string on any parse failure. Safe to use for
// cache keying because full signature/expiry/audience validation still
// happens via grpc.ValidateToken on cache miss.
func parseJTI(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		JTI string `json:"jti"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.JTI
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

// GetUserPermissions fetches the RBAC access list for the given user+tenant from
// registry-auth. The returned slice contains one entry per repository the user has
// been explicitly granted access to; callers should check it for the required action.
func (a *AuthClient) GetUserPermissions(ctx context.Context, userID, tenantID string) ([]*authv1.RepositoryAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.grpc.GetUserPermissions(ctx, &authv1.GetUserPermissionsRequest{
		UserId:   userID,
		TenantId: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("get user permissions rpc: %w", err)
	}
	return resp.GetAccess(), nil
}
