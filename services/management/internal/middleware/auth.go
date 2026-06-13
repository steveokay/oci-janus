// Package middleware provides HTTP middleware for registry-management.
package middleware

import (
	"context"
	"net/http"
	"strings"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

type contextKey string

const (
	contextKeyTenantID contextKey = "tenant_id"
	contextKeyUserID   contextKey = "user_id"
)

// RequireAuth validates the Bearer token in the Authorization header against
// the auth service gRPC. Injects tenant_id and user_id into the request
// context on success; returns 401 on any failure. Never passes a request
// without a valid, non-expired token to downstream handlers.
func RequireAuth(authClient authv1.AuthServiceClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}

			resp, err := authClient.ValidateToken(r.Context(), &authv1.ValidateTokenRequest{Token: token})
			if err != nil || !resp.GetValid() {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), contextKeyTenantID, resp.GetTenantId())
			ctx = context.WithValue(ctx, contextKeyUserID, resp.GetUserId())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TenantIDFromContext returns the tenant ID injected by RequireAuth.
// Returns an empty string if the context was not populated (should not happen
// on any route protected by RequireAuth).
func TenantIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyTenantID).(string)
	return v
}

// UserIDFromContext returns the user ID injected by RequireAuth.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyUserID).(string)
	return v
}
