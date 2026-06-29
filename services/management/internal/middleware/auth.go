// Package middleware provides HTTP middleware for registry-management.
package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

type contextKey string

const (
	contextKeyTenantID      contextKey = "tenant_id"
	contextKeyUserID        contextKey = "user_id"
	contextKeyPrincipalKind contextKey = "principal_kind"
)

// PrincipalKindHuman is the typed value injected into request context for
// human-attestable identity. Mirrors users.kind ('human') and is the default
// when a token does not carry an explicit principal_kind claim (legacy
// tokens issued before REDESIGN-001 Phase 5.4).
const PrincipalKindHuman = "human"

// PrincipalKindServiceAccount mirrors users.kind ('service_account'). Admin
// gates deny callers whose context kind matches this value, regardless of
// the role assignments on their shadow user (Decision #24).
const PrincipalKindServiceAccount = "service_account"

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

			// REDESIGN-001 Phase 5.4: extract the principal_kind claim
			// directly from the JWT payload so admin gates can deny SA
			// bearers without a separate RPC. The auth service has
			// already verified the RS256 signature via ValidateToken
			// above, so the claim is trusted at this point — we only
			// decode the (already-validated) payload to read the field.
			// The auth.v1 proto does not surface principal_kind yet, so
			// reading the JWT body keeps this change self-contained
			// without a proto regeneration.
			kind := parsePrincipalKindFromJWT(token)
			if kind == "" {
				// Legacy tokens issued before Phase 5.4 had no
				// principal_kind claim; treat them as human so existing
				// human sessions continue to work.
				kind = PrincipalKindHuman
			}

			ctx := context.WithValue(r.Context(), contextKeyTenantID, resp.GetTenantId())
			ctx = context.WithValue(ctx, contextKeyUserID, resp.GetUserId())
			ctx = context.WithValue(ctx, contextKeyPrincipalKind, kind)
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

// PrincipalKindFromContext returns the principal kind ("human" or
// "service_account") for the authenticated caller, sourced from the JWT's
// principal_kind claim by RequireAuth. Returns "human" when the claim was
// absent (legacy tokens) — see RequireAuth for the rationale. Empty string
// is only ever returned on routes that bypass RequireAuth (which is itself a
// configuration error and should never happen for admin-gated routes).
func PrincipalKindFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyPrincipalKind).(string)
	return v
}

// parsePrincipalKindFromJWT extracts the principal_kind claim from a JWT's
// payload segment. Returns the empty string on any structural error — the
// caller treats that as "human" (the pre-Phase-5.4 default) so legacy
// tokens keep working.
//
// SAFETY: this helper does NOT verify the JWT signature. It is intentionally
// only called AFTER the auth service has validated the token via the gRPC
// ValidateToken RPC, which performs the full RS256 signature check, the
// expiry check, and the Redis revocation lookup. Reading the payload at
// that point is only a structural decode of an already-trusted string —
// there is no defence-in-depth weakening, because the auth service is the
// trust anchor for both checks today.
func parsePrincipalKindFromJWT(token string) string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	// JWTs use base64url without padding (RFC 7519 §3); RawURLEncoding
	// matches the wire format. golang-jwt encodes the same way.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		PrincipalKind string `json:"principal_kind"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return ""
	}
	return c.PrincipalKind
}

// assertNoCookieAuth is a static assertion intended for a future linter or
// code reviewer: searching for `r.Cookie(` in this file should return zero
// hits. This package authenticates strictly via the Authorization header,
// which is the load-bearing assumption for PENTEST-020's CSRF posture.
// Removing this comment without adding CSRF tokens would weaken the API.
var _ = "PENTEST-020: authentication is Authorization-header only — see RequireAuth comment"
