package grpc

// REDESIGN-001 Phase 6.10 — mTLS peer-CN allowlist interceptor.
//
// Before this interceptor existed, the only check a gRPC server performed on a
// caller's client certificate was that it was signed by the platform CA
// (RequireAndVerifyClientCert in libs/auth/mtls). That is sufficient to keep
// outsiders out, but it does NOT stop a CA-signed peer from calling endpoints
// it has no business invoking — e.g. registry-gc reaching for
// registry-auth.GrantRole.
//
// This file adds a second-stage authorisation hook: per-server allowlist of
// peer Common Names. If the allowlist is empty the interceptor is a no-op
// (Option A, see commit message + Phase 6.10 entry in the redesign plan); when
// populated it returns codes.PermissionDenied for any caller whose cert CN is
// not in the list. Rejection is logged with method + CN so operators can see
// the call attempt and decide whether to grant access.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/observability/metrics"
)

// peerCNAllowlistEnvVar is the canonical environment variable each service
// reads (or has read for it by the loader) to populate its peer-CN allowlist.
// Format: comma-separated list of expected CNs, e.g.
//
//	MTLS_PEER_CN_ALLOWLIST=registry-core,registry-management
//
// Whitespace around individual entries is trimmed; empty entries are dropped.
//
// CN comparison is case-sensitive (`registry-auth` ≠ `Registry-Auth`). The
// platform's gen-dev-certs.sh + cert-manager templates both emit lowercase
// `registry-<svc>` CNs, so case-sensitivity matches reality; if you set a
// mixed-case entry you'll silently get a denial.
const peerCNAllowlistEnvVar = "MTLS_PEER_CN_ALLOWLIST"

// otelEnvironmentEnvVar mirrors the project-wide OTEL_ENVIRONMENT setting
// (see CLAUDE.md §10) — checked at allowlist-constructor time so we can WARN
// loudly when production starts up with the allowlist still unset.
const otelEnvironmentEnvVar = "OTEL_ENVIRONMENT"

// peerCNAllowlistStateLog ensures the "allowlist enabled" / "allowlist
// disabled" startup message is emitted at most once per process, even when a
// server wires both PeerCNAllowlist + PeerCNAllowlistStream (each constructor
// calls logAllowlistState — the sync.Once collapses to a single emission).
var peerCNAllowlistStateLog sync.Once

// PeerCNAllowlist returns a UnaryServerInterceptor that rejects RPCs whose
// caller does not present a TLS client certificate with a Common Name in
// `allowed`. When `allowed` is empty the interceptor is a no-op (Option A —
// backwards compatible: operators opt in per service via MTLS_PEER_CN_ALLOWLIST
// before enforcement turns on for that server).
//
// Order: install AFTER the standard mTLS CA verification (which happens at the
// TLS handshake layer via tls.RequireAndVerifyClientCert) so this interceptor
// can rely on PeerCertificates[0] being a trusted, CA-signed cert.
//
// Failure modes:
//   - missing peer info (no TLS) → PermissionDenied (defence in depth, should
//     never happen on a properly configured mTLS server)
//   - empty PeerCertificates       → PermissionDenied
//   - CN not in allowlist          → PermissionDenied
//
// The error returned to the caller deliberately omits the offending CN so a
// malicious peer cannot probe the allowlist; the CN is captured on the
// server-side log instead.
func PeerCNAllowlist(allowed ...string) grpc.UnaryServerInterceptor {
	// Pre-compute the allowed set so each RPC is an O(1) map lookup rather than
	// an O(n) slice scan. Building once at construction also lets us trim/dedupe
	// once instead of per-call.
	allowedSet := buildAllowedSet(allowed)

	// Constructor-time observability so the configuration state is visible
	// without waiting for the first RPC (SEC-044 follow-up). The gauge is
	// always set so an alert can fire on `== 0`. The startup WARN fires loudly
	// when production starts with no allowlist, so a misconfigured deploy
	// surfaces in the deploy log rather than dribbling out as INFO on RPC 1.
	logAllowlistState(allowedSet)

	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		// Empty allowlist == no enforcement (Option A). The "disabled" state is
		// already logged + reflected in the GRPCPeerCNAllowlistEnabled gauge at
		// constructor time; pass through without per-RPC noise.
		if len(allowedSet) == 0 {
			return handler(ctx, req)
		}

		// Extract the peer cert CN. Anything anomalous (no peer, no TLS info,
		// no cert chain) is treated as a denied call — fail-closed once an
		// allowlist is configured.
		cn, ok := peerCommonName(ctx)
		if !ok {
			slog.Warn("grpc peer CN missing — rejecting RPC",
				"method", info.FullMethod,
			)
			metrics.GRPCPeerCNDeniedTotal.WithLabelValues(info.FullMethod, "missing_cn").Inc()
			return nil, status.Error(codes.PermissionDenied, "peer not in allowlist")
		}

		if _, allowed := allowedSet[cn]; !allowed {
			// Log the rejection at WARN with method + CN so operators can run a
			// quick grep to see "registry-X tried to call /pkg.Service/Method"
			// and decide whether to add X to the allowlist. The caller-facing
			// error message does NOT include the CN — denying probes.
			slog.Warn("grpc peer CN rejected by allowlist",
				"method", info.FullMethod,
				"peer_cn", cn,
			)
			metrics.GRPCPeerCNDeniedTotal.WithLabelValues(info.FullMethod, "cn_not_allowed").Inc()
			return nil, status.Error(codes.PermissionDenied, "peer not in allowlist")
		}

		return handler(ctx, req)
	}
}

// PeerCNAllowlistStream is the stream-server equivalent of PeerCNAllowlist.
// Same semantics, same error codes, same fail-closed behaviour.
func PeerCNAllowlistStream(allowed ...string) grpc.StreamServerInterceptor {
	allowedSet := buildAllowedSet(allowed)

	// Constructor-time observability — see PeerCNAllowlist for rationale. The
	// sync.Once gating inside logAllowlistState means a server running both
	// unary + stream interceptors logs the state exactly once.
	logAllowlistState(allowedSet)

	return func(
		srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		// Match the unary behaviour — empty allowlist is a no-op (already
		// logged + gauge-set at constructor time, no per-RPC logging).
		if len(allowedSet) == 0 {
			return handler(srv, ss)
		}

		cn, ok := peerCommonName(ss.Context())
		if !ok {
			slog.Warn("grpc peer CN missing — rejecting stream",
				"method", info.FullMethod,
			)
			metrics.GRPCPeerCNDeniedTotal.WithLabelValues(info.FullMethod, "missing_cn").Inc()
			return status.Error(codes.PermissionDenied, "peer not in allowlist")
		}

		if _, allowed := allowedSet[cn]; !allowed {
			slog.Warn("grpc peer CN rejected by allowlist",
				"method", info.FullMethod,
				"peer_cn", cn,
			)
			metrics.GRPCPeerCNDeniedTotal.WithLabelValues(info.FullMethod, "cn_not_allowed").Inc()
			return status.Error(codes.PermissionDenied, "peer not in allowlist")
		}

		return handler(srv, ss)
	}
}

// PeerCNAllowlistFromEnv reads MTLS_PEER_CN_ALLOWLIST from the process
// environment and returns the configured interceptor. Use this constructor in
// service main.go so the allowlist stays declarative (no hardcoded list of
// peer service names baked into individual servers).
//
// Empty / unset env var → empty allowlist → no-op interceptor (Option A).
func PeerCNAllowlistFromEnv() grpc.UnaryServerInterceptor {
	return PeerCNAllowlist(parsePeerCNAllowlist(os.Getenv(peerCNAllowlistEnvVar))...)
}

// PeerCNAllowlistStreamFromEnv is the stream-server twin of
// PeerCNAllowlistFromEnv.
func PeerCNAllowlistStreamFromEnv() grpc.StreamServerInterceptor {
	return PeerCNAllowlistStream(parsePeerCNAllowlist(os.Getenv(peerCNAllowlistEnvVar))...)
}

// parsePeerCNAllowlist splits the CSV env var on commas, trims whitespace, and
// drops empty entries. Pulled out so the unary + stream constructors share
// exactly one parsing implementation.
func parsePeerCNAllowlist(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// buildAllowedSet converts the public ...string parameter into a set with the
// same trim-and-dedupe semantics parsePeerCNAllowlist uses. Both
// PeerCNAllowlist(["a", " a "]) and PeerCNAllowlist(["a", "a"]) collapse to a
// single entry so callers never accidentally widen the allowlist via
// whitespace or duplication.
func buildAllowedSet(allowed []string) map[string]struct{} {
	if len(allowed) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(allowed))
	for _, cn := range allowed {
		if trimmed := strings.TrimSpace(cn); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

// logAllowlistState is called once at interceptor construction (per server,
// per kind) to surface the current allowlist posture in logs + metrics. The
// sync.Once gate means a server that wires both PeerCNAllowlist and
// PeerCNAllowlistStream emits the state exactly once.
//
// SEC-044 follow-up: when the allowlist is empty AND OTEL_ENVIRONMENT=production,
// we WARN instead of INFO so a misconfigured deploy ("we forgot to set
// MTLS_PEER_CN_ALLOWLIST in prod") is visible in the deploy log, not lost in
// per-RPC noise. The GRPCPeerCNAllowlistEnabled gauge is set unconditionally
// so an alert can fire on `== 0` without needing log parsing.
func logAllowlistState(allowedSet map[string]struct{}) {
	peerCNAllowlistStateLog.Do(func() {
		if len(allowedSet) == 0 {
			metrics.GRPCPeerCNAllowlistEnabled.Set(0)
			if strings.EqualFold(os.Getenv(otelEnvironmentEnvVar), "production") {
				slog.Warn("grpc peer CN allowlist disabled in production — set MTLS_PEER_CN_ALLOWLIST to enforce per-peer identity",
					"env", "production",
				)
			} else {
				slog.Info("grpc peer CN allowlist disabled — set MTLS_PEER_CN_ALLOWLIST to enable")
			}
			return
		}
		metrics.GRPCPeerCNAllowlistEnabled.Set(1)
		slog.Info("grpc peer CN allowlist enabled",
			"peer_count", len(allowedSet),
		)
	})
}

// peerCommonName extracts the Common Name from the first peer certificate on
// the call context. Returns ok=false when there is no peer, no TLS info, no
// cert chain, or the CN is empty — every one of those is treated as "deny" by
// the caller once an allowlist is configured.
//
// We deliberately use only PeerCertificates[0] (the leaf): client cert chains
// in this platform are issued directly off the internal CA, so the leaf CN is
// the service identity. If that ever changes (e.g. intermediate CAs), this
// helper is the single place to update.
func peerCommonName(ctx context.Context) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", false
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", false
	}
	cn := tlsInfo.State.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", false
	}
	return cn, true
}
