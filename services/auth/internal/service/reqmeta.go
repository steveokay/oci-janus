// Package service — reqmeta.go
//
// Request-scoped actor context (client source IP + authenticating API-key id)
// carried through context.Context from the HTTP handler down to the audit
// emitter. Keeping it in ctx lets the emitter stamp lifecycle events without
// threading extra params through every ServiceAccountService method.
package service

import "context"

type reqMetaKey struct{}

// requestMeta is the value stored in context. Both fields are best-effort:
// sourceIP is the trusted-proxy-resolved client IP; apiKeyID is the id of the
// API key that authenticated the request, empty for JWT/browser sessions.
type requestMeta struct {
	sourceIP string
	apiKeyID string
}

// WithRequestMeta returns a child context carrying the request's source IP and
// authenticating API-key id (either may be empty).
func WithRequestMeta(ctx context.Context, sourceIP, apiKeyID string) context.Context {
	return context.WithValue(ctx, reqMetaKey{}, requestMeta{sourceIP: sourceIP, apiKeyID: apiKeyID})
}

// RequestMetaFromContext returns (sourceIP, apiKeyID). Missing values are the
// empty string — never panics on a bare context.
func RequestMetaFromContext(ctx context.Context) (sourceIP, apiKeyID string) {
	m, _ := ctx.Value(reqMetaKey{}).(requestMeta)
	return m.sourceIP, m.apiKeyID
}
