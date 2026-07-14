package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// REDESIGN-001 Phase 3.3 — single-tenant context injector.
//
// The whole system already filters every query by tenant_id, but the value
// is constant (the platform hosts exactly one tenant — ADR-0031) and the
// FE/BFF could either forget to send it (a regression) or send a stale UUID
// (e.g. a tenant rename in dev where a browser tab kept the old id). This
// middleware is defence-in-depth on the gRPC plane: it normalises the
// inbound x-tenant-id metadata to the bootstrap tenant id so downstream
// queries can trust the context, and it rejects requests that ship a
// CONFLICTING tenant_id with a clear error instead of silently routing them
// somewhere they don't belong.
//
// Phase 9.3 (ADR-0031) made injection unconditional: every service wires
// this interceptor at startup once it has fetched the bootstrap tenant id.
// An empty bootstrapTenantID is now only a defensive pre-bootstrap shape
// (see SingleTenantInjector) — not a "multi mode" the platform still ships.

// tenantIDMetadataKey is the gRPC metadata key carrying the active tenant
// identity on every RPC. Lowercase per HTTP/2 wire format; gRPC's metadata
// API normalises keys to lowercase but we spell it out so the constant
// matches the on-wire bytes if anyone greps for it.
const tenantIDMetadataKey = "x-tenant-id"

// TenantIDFromIncomingContext returns the x-tenant-id metadata value on the
// inbound gRPC context, or "" if absent. SingleTenantInjector has already
// populated it with the bootstrap tenant id, so a handler serving an
// unauthenticated/tenant-less caller (e.g. the FUT-023 SCM webhook, which
// carries no JWT) can recover the active tenant from the context instead of
// the request body. Returns "" only in the defensive pre-bootstrap shape
// where the injector was wired with an empty id.
func TenantIDFromIncomingContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if values := md.Get(tenantIDMetadataKey); len(values) > 0 {
		return values[0]
	}
	return ""
}

// SingleTenantInjector returns a unary interceptor that enforces a single
// canonical tenant_id. Every service wires it unconditionally once it has
// resolved the bootstrap tenant id (ADR-0031 / Phase 9.3).
//
// Unary only by design: streams in this codebase carry tenant_id in the
// first request message (not metadata), so a stream variant would be a
// no-op. If a future RPC ships header-based stream tenant routing, add
// a SingleTenantStreamInjector then — don't pre-add it here.
//
// Behaviour matrix:
//
//	bootstrapTenantID == ""  → defensive passthrough. Not a supported mode:
//	                            it only occurs before the platform has been
//	                            bootstrapped (or if a caller ignores the
//	                            fetch error). The interceptor passes every
//	                            request through untouched rather than force
//	                            the caller to nil-check.
//	x-tenant-id absent       → inject bootstrapTenantID into the metadata so
//	                            downstream handlers see a populated value.
//	x-tenant-id == bootstrap → pass through unchanged.
//	x-tenant-id != bootstrap → log a warning, reject with InvalidArgument.
//	                            The mismatched UUID is logged (it's not a
//	                            secret — every authenticated caller can read
//	                            their own tenant id from /users/me) so an
//	                            operator chasing a "why is my CLI 400-ing"
//	                            ticket can see the offending value.
//
// Wire into a server like:
//
//	grpcSrv := grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(
//	        append(grpcmw.ServerInterceptors(),
//	            grpcmw.SingleTenantInjector(cfg.BootstrapTenantID),
//	        )...,
//	    ),
//	)
//
// The bootstrap tenant id is supplied by the caller (usually read at
// startup from services/tenant.deployment_metadata.bootstrap_tenant_id);
// this package deliberately does not fetch it itself to keep the lib free
// of a hard dependency on the tenant service.
func SingleTenantInjector(bootstrapTenantID string) grpc.UnaryServerInterceptor {
	if bootstrapTenantID == "" {
		// Defensive pre-bootstrap shape only — the platform is single-tenant
		// (ADR-0031) and every caller fetches a real bootstrap id before
		// wiring this. Return the identity interceptor so the chain still
		// works without a nil check on the caller.
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, hadMetadata := metadata.FromIncomingContext(ctx)
		if !hadMetadata {
			md = metadata.New(nil)
		}

		values := md.Get(tenantIDMetadataKey)
		switch {
		case len(values) == 0 || values[0] == "":
			// Missing — inject bootstrap id so downstream handlers can
			// trust the context. Set (replace) rather than append so we
			// don't end up with a list of empty + populated entries.
			md.Set(tenantIDMetadataKey, bootstrapTenantID)
			ctx = metadata.NewIncomingContext(ctx, md)
		case values[0] == bootstrapTenantID:
			// Match — pass through unchanged.
		default:
			// Mismatch — defence in depth fires. Caller asked for a tenant
			// the deployment doesn't host. Log + reject; do NOT silently
			// rewrite to the bootstrap id (that would mask FE/BFF bugs).
			slog.WarnContext(ctx, "single-tenant injector rejected mismatched tenant id",
				"method", info.FullMethod,
				"got_tenant_id", values[0],
				"bootstrap_tenant_id", bootstrapTenantID,
			)
			return nil, status.Errorf(codes.InvalidArgument,
				"this deployment hosts a single tenant (%s); request supplied %q",
				bootstrapTenantID, values[0])
		}

		return handler(ctx, req)
	}
}
