// Package service — oidc_exchange.go is the public token-exchange path
// for FUT-001 federated workload identity. A CI runner POSTs its OIDC
// JWT to /auth/token/workload; this code parses + validates it, finds
// the matching trust config, verifies the signature against the IdP's
// JWKS, and mints a 15-minute RS256 registry JWT keyed to the trust's
// service account.
//
// SECURITY: every rejection path collapses to a generic
// codes.Unauthenticated so the response body does NOT leak which gate
// failed. The full classification is emitted to the audit event (so
// forensics can distinguish "wrong issuer" from "wrong subject") but
// the CI runner only sees "unauthorized."
//
// The 7 named reject reasons (matching the spec):
//
//  1. issuer_not_allowed    — `iss` not in OIDC_ALLOWED_ISSUERS
//  2. audience_mismatch     — `aud` doesn't match any trust's audience
//  3. subject_mismatch      — `sub` doesn't match any trust's pattern
//  4. signature_invalid     — RS256 verify failed (incl. unknown kid)
//  5. expired               — `exp` in the past
//  6. not_yet_valid         — `nbf` in the future
//  7. sa_disabled           — trust's SA has been disabled
//
// Each branch emits an auth.workload_token.rejected event with the
// reason set to the matching string.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// WorkloadTokenResult is the synthesised access-token returned to the
// caller. Mirrors the OAuth 2.0 token response shape.
type WorkloadTokenResult struct {
	AccessToken string
	ExpiresIn   int32 // seconds
	TokenType   string
}

// rejectReason enumerates the 7 audit reasons. Reported to the audit
// event but NOT to the caller — the caller always gets a generic
// codes.Unauthenticated to avoid leaking which gate failed.
type rejectReason string

const (
	rejectIssuerNotAllowed rejectReason = "issuer_not_allowed"
	rejectAudienceMismatch rejectReason = "audience_mismatch"
	rejectSubjectMismatch  rejectReason = "subject_mismatch"
	rejectSignatureInvalid rejectReason = "signature_invalid"
	rejectExpired          rejectReason = "expired"
	rejectNotYetValid      rejectReason = "not_yet_valid"
	rejectSADisabled       rejectReason = "sa_disabled"
)

// maxSubjectLogLen caps the audit-event `subject` field so a hostile
// caller cannot inflate audit rows with a multi-MB sub claim. 256 chars
// is comfortably larger than every realistic CI subject (~120 chars for
// GitHub Actions, ~100 for GitLab) without risking row-size pressure.
const maxSubjectLogLen = 256

// ExchangeWorkloadToken is the FUT-001 token-exchange entry point.
// On success: emits auth.workload_token.exchanged and returns the
// minted registry JWT.
// On failure: emits auth.workload_token.rejected with a reason and
// returns codes.Unauthenticated (or codes.Unavailable if the IdP is
// unreachable).
func (s *OIDCTrustService) ExchangeWorkloadToken(ctx context.Context, rawJWT string) (*WorkloadTokenResult, error) {
	if rawJWT == "" {
		return s.rejectAndEmit(ctx, "", "", nil, rejectSignatureInvalid)
	}

	// Step 1: parse WITHOUT verifying so we can read iss/sub/aud to
	// find a matching trust. The actual signature verification happens
	// later, once we know which JWKS to fetch.
	unverified, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(rawJWT, jwt.MapClaims{})
	if err != nil {
		return s.rejectAndEmit(ctx, "", "", nil, rejectSignatureInvalid)
	}
	claims, _ := unverified.Claims.(jwt.MapClaims)
	iss, _ := claims["iss"].(string)
	sub, _ := claims["sub"].(string)
	aud := firstAudience(claims)

	// Step 2: issuer allowlist gate. Runs BEFORE any DB lookup so an
	// attacker who can fire arbitrary issuer URLs at us cannot drive
	// per-request DB load.
	if !issuerAllowed(s.allowedIssuers, iss) {
		return s.rejectAndEmit(ctx, iss, sub, nil, rejectIssuerNotAllowed)
	}

	// Step 3: find a matching trust config. Multiple trusts may share
	// an issuer (e.g. one workspace federating two different GitHub
	// repos against the same IdP). The first trust whose audience
	// equals the token's aud AND whose subject_pattern matches sub
	// wins. Order is created_at DESC so the most recently-added trust
	// takes precedence when a pattern overlap exists.
	candidates, err := s.repo.ListByIssuer(ctx, iss)
	if err != nil {
		// DB outage on the exchange path is not the user's fault —
		// surface it as Unavailable rather than Unauthenticated so
		// the CI retries.
		return nil, status.Errorf(codes.Unavailable, "trust lookup failed: %v", err)
	}
	var matched *repository.OIDCTrust
	audAny := false
	for _, t := range candidates {
		if t.Audience == aud {
			audAny = true
			if subjectMatches(t.SubjectPattern, sub) {
				matched = t
				break
			}
		}
	}
	if matched == nil {
		// Bias the audit classification toward the more informative
		// reason: if any trust matched the audience but none matched
		// the subject, the admin probably mis-typed the pattern. If
		// nothing matched the audience, the JWT was minted for a
		// different audience than the trust expects.
		if audAny {
			return s.rejectAndEmit(ctx, iss, sub, nil, rejectSubjectMismatch)
		}
		return s.rejectAndEmit(ctx, iss, sub, nil, rejectAudienceMismatch)
	}

	// Step 4: fetch + cache JWKS for this issuer.
	ttl := time.Duration(matched.JWKSCacheTTLSeconds) * time.Second
	keys, err := s.jwks.Fetch(ctx, iss, ttl)
	if err != nil {
		// Network failure or malformed JWKS — surface as Unavailable
		// so the CI retries with backoff.
		return nil, status.Errorf(codes.Unavailable, "fetch jwks: %v", err)
	}

	// Step 5: verify the signature. We require RS256 explicitly so an
	// attacker cannot downgrade to "alg: none" or HS256 with a
	// known-secret attack.
	parsed, err := jwt.Parse(rawJWT, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", tok.Header["alg"])
		}
		kid, _ := tok.Header["kid"].(string)
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("kid %q not found in JWKS", kid)
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return s.rejectAndEmit(ctx, iss, sub, matched, rejectExpired)
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			return s.rejectAndEmit(ctx, iss, sub, matched, rejectNotYetValid)
		default:
			return s.rejectAndEmit(ctx, iss, sub, matched, rejectSignatureInvalid)
		}
	}
	if !parsed.Valid {
		return s.rejectAndEmit(ctx, iss, sub, matched, rejectSignatureInvalid)
	}

	// Step 6: load + validate the SA. A disabled SA short-circuits even
	// after a valid signature — operators expect "disable the SA" to
	// kill workload tokens immediately.
	sa, err := s.serviceAccounts.Get(ctx, matched.ServiceAccountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// SA deleted but trust still in DB — extremely rare (the
			// FK CASCADE should have cleaned this up) but treat as SA
			// disabled so the audit event is informative.
			return s.rejectAndEmit(ctx, iss, sub, matched, rejectSADisabled)
		}
		return nil, status.Errorf(codes.Unavailable, "load sa: %v", err)
	}
	if sa.DisabledAt != nil {
		return s.rejectAndEmit(ctx, iss, sub, matched, rejectSADisabled)
	}

	// Step 7: mint the workload token. Access is built from the SA's
	// allowed_scopes — same shape ValidateAPIKey uses for SA-owned
	// API keys so downstream services see a consistent identity.
	access := mapScopesToAccess(sa.AllowedScopes)
	accessToken, err := s.auth.IssueWorkloadToken(ctx, sa.ShadowUserID.String(), sa.TenantID.String(), matched.ID.String(), access)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue workload token: %v", err)
	}

	// Step 8: best-effort last_used_at writeback. A failure here does
	// NOT block the response — the exchange has succeeded.
	if err := s.repo.MarkUsed(ctx, matched.ID); err != nil {
		slog.WarnContext(ctx, "oidc exchange: MarkUsed failed",
			"trust_id", matched.ID,
			"err", err,
		)
	}

	// Step 9: emit success audit. Best-effort — a broker outage must
	// not roll back the token mint (the JWT is already in the response
	// body's pipeline by the time the audit emit can fail).
	s.emitExchangeSuccess(ctx, iss, sub, matched, sa)

	return &WorkloadTokenResult{
		AccessToken: accessToken,
		ExpiresIn:   int32(WorkloadTokenLifetimeSeconds),
		TokenType:   "Bearer",
	}, nil
}

// firstAudience extracts the first `aud` value from the JWT claims map.
// The aud claim is either a string OR a string[] per RFC 7519. We accept
// both and return the first entry of the array form.
func firstAudience(claims jwt.MapClaims) string {
	switch v := claims["aud"].(type) {
	case string:
		return v
	case []any:
		if len(v) == 0 {
			return ""
		}
		s, _ := v[0].(string)
		return s
	case []string:
		if len(v) == 0 {
			return ""
		}
		return v[0]
	default:
		return ""
	}
}

// rejectAndEmit emits an auth.workload_token.rejected audit event and
// returns the generic codes.Unauthenticated. The trust pointer may be
// nil when the reject happened before a trust could be matched (issuer
// allowlist failure, audience mismatch, parse failure).
func (s *OIDCTrustService) rejectAndEmit(ctx context.Context, iss, sub string, trust *repository.OIDCTrust, reason rejectReason) (*WorkloadTokenResult, error) {
	s.emitExchangeRejected(ctx, iss, sub, trust, reason)
	return nil, status.Error(codes.Unauthenticated, "workload token rejected")
}

// emitExchangeSuccess emits auth.workload_token.exchanged with the trust
// + SA identity. Best-effort.
func (s *OIDCTrustService) emitExchangeSuccess(ctx context.Context, iss, sub string, trust *repository.OIDCTrust, sa *repository.ServiceAccount) {
	if s.audit == nil {
		return
	}
	ev := AuditEvent{
		TenantID: trust.TenantID.String(),
		Action:   "auth.workload_token.exchanged",
		ActorID:  sa.ShadowUserID.String(),
		Resource: trust.ID.String(),
		Fields: map[string]any{
			"trust_id":           trust.ID.String(),
			"issuer_url":         iss,
			"subject":            truncate(sub, maxSubjectLogLen),
			"service_account_id": sa.ID.String(),
		},
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "oidc exchange: success audit emit failed",
			"trust_id", trust.ID,
			"err", err,
		)
	}
}

// emitExchangeRejected emits auth.workload_token.rejected with the
// classification reason. The trust pointer may be nil for early rejects.
// TenantID is taken from the trust when available, empty otherwise — an
// audit event without a tenant_id is dropped by the consumer (the
// consumer treats it as a malformed event and ACKs to skip), but the
// slog warning on the dropped event surfaces an audit-feed gap to
// operators.
func (s *OIDCTrustService) emitExchangeRejected(ctx context.Context, iss, sub string, trust *repository.OIDCTrust, reason rejectReason) {
	if s.audit == nil {
		return
	}
	fields := map[string]any{
		"issuer_url": iss,
		"subject":    truncate(sub, maxSubjectLogLen),
		"reason":     string(reason),
	}
	tenantID := ""
	resource := ""
	actorID := "anonymous"
	if trust != nil {
		tenantID = trust.TenantID.String()
		resource = trust.ID.String()
		fields["trust_id"] = trust.ID.String()
		fields["service_account_id"] = trust.ServiceAccountID.String()
	}
	ev := AuditEvent{
		TenantID: tenantID,
		Action:   "auth.workload_token.rejected",
		ActorID:  actorID,
		Resource: resource,
		Fields:   fields,
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "oidc exchange: reject audit emit failed",
			"reason", reason,
			"err", err,
		)
	}
}

// truncate clips a string to at most n bytes. Used for the audit-event
// subject field so a hostile caller cannot inflate row size.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
