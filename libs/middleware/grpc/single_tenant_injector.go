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
// The whole system already filters every query by tenant_id, but in single
// mode the value is constant and the FE/BFF could either forget to send it
// (a regression) or send a stale UUID (e.g. a tenant rename in dev where a
// browser tab kept the old id). This middleware is defence-in-depth on the
// gRPC plane: it normalises the inbound x-tenant-id metadata to the
// bootstrap tenant id so downstream queries can trust the context, and it
// rejects requests that ship a CONFLICTING tenant_id with a clear error
// instead of silently routing them somewhere they don't belong.
//
// The interceptor is a no-op in multi mode — the value of bootstrapTenantID
// drives the behaviour (empty string ⇒ no-op).

// tenantIDMetadataKey is the gRPC metadata key carrying the active tenant
// identity on every RPC. Lowercase per HTTP/2 wire format; gRPC's metadata
// API normalises keys to lowercase but we spell it out so the constant
// matches the on-wire bytes if anyone greps for it.
const tenantIDMetadataKey = "x-tenant-id"

// SingleTenantInjector returns a unary interceptor that enforces a single
// canonical tenant_id when bootstrapTenantID is non-empty (single mode).
//
// Behaviour matrix:
//
//	bootstrapTenantID == ""  → no-op (multi mode); the interceptor passes
//	                            every request through untouched.
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
		// Multi mode (or "I haven't bootstrapped yet" — same shape from
		// the middleware's perspective). Return the identity interceptor
		// so the chain still works without a nil check on the caller.
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
				"DEPLOYMENT_MODE=single hosts a single tenant (%s); request supplied %q",
				bootstrapTenantID, values[0])
		}

		return handler(ctx, req)
	}
}
