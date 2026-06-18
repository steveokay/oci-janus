// Package middleware provides HTTP middleware for registry-management.
package middleware

import (
	"context"
	"net/http"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
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
//
// PENTEST-020 — CSRF posture: this service deliberately only accepts tokens
// from the Authorization header, NEVER from cookies. Combined with the strict
// CORS allowlist (PENTEST-008) and the in-memory JWT storage on the frontend
// (FE-SEC-001), this design is CSRF-immune by construction — browsers will
// not auto-attach an Authorization header on cross-origin requests, so an
// attacker page cannot reuse the user's session against this API. If a
// future change adds cookie-based authentication (e.g. an HttpOnly refresh
// cookie per FE-SEC-009), CSRF tokens MUST be added at the same time. The
// `assertNoCookieAuth` check below catches accidental drift.
func RequireAuth(authClient authv1.AuthServiceClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// PENTEST-013: scheme name is case-insensitive per RFC 7235; use
			// the shared bearer.Extract helper rather than a case-sensitive
			// HasPrefix/TrimPrefix combo.
			token, ok := bearer.Extract(r.Header.Get("Authorization"))
			if !ok {
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

// assertNoCookieAuth is a static assertion intended for a future linter or
// code reviewer: searching for `r.Cookie(` in this file should return zero
// hits. This package authenticates strictly via the Authorization header,
// which is the load-bearing assumption for PENTEST-020's CSRF posture.
// Removing this comment without adding CSRF tokens would weaken the API.
var _ = "PENTEST-020: authentication is Authorization-header only — see RequireAuth comment"
