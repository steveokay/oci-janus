// Package grpc provides gRPC interceptor chains for all registry services.
// Every service applies these via grpc.ChainUnaryInterceptor /
// grpc.ChainStreamInterceptor — never register interceptors ad-hoc.
package grpc

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// grpcDurationHist is initialized once — on the first RPC after OTEL bootstrap —
// to avoid a meter lookup + error allocation on every call.
var (
	grpcDurationHistOnce sync.Once
	grpcDurationHist     metric.Float64Histogram
)

func initGRPCDurationHist() metric.Float64Histogram {
	grpcDurationHistOnce.Do(func() {
		h, err := otel.GetMeterProvider().Meter("registry").Float64Histogram(
			"registry_grpc_request_duration_seconds",
			metric.WithDescription("Duration of gRPC requests in seconds"),
			metric.WithUnit("s"),
		)
		if err != nil {
			slog.Warn("grpc metrics: histogram init failed", "error", err)
			return
		}
		grpcDurationHist = h
	})
	return grpcDurationHist
}

// OTELServerHandler returns a grpc.ServerOption that installs the OpenTelemetry
// stats handler. Attach this alongside ChainUnaryInterceptor — the stats handler
// approach (replacing the old interceptor-based API) avoids deprecation warnings
// and correctly handles streaming RPCs.
func OTELServerHandler() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(
		otelgrpc.WithTracerProvider(otel.GetTracerProvider()),
		otelgrpc.WithMeterProvider(otel.GetMeterProvider()),
	))
}

// ctxKey is a private type so request-ID keys don't collide with other packages.
type ctxKey string

const requestIDKey ctxKey = "request_id"

// RequestIDFromContext returns the request ID injected by RequestIDInterceptor,
// or an empty string if the context has no request ID.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// ServerInterceptors returns the ordered unary interceptor chain applied to
// every gRPC server in the registry. The order is significant — recovery must
// be outermost so it catches panics from any later interceptor or handler.
// OTEL tracing is handled separately via OTELServerHandler() as a StatsHandler.
func ServerInterceptors() []grpc.UnaryServerInterceptor {
	return []grpc.UnaryServerInterceptor{
		RecoveryInterceptor,
		RequestIDInterceptor,
		LoggingInterceptor,
		MetricsInterceptor,
	}
}

// StreamServerInterceptors returns the ordered stream interceptor chain.
func StreamServerInterceptors() []grpc.StreamServerInterceptor {
	return []grpc.StreamServerInterceptor{
		RecoveryStreamInterceptor,
		LoggingStreamInterceptor,
	}
}

// RecoveryInterceptor converts panics in handlers to codes.Internal errors so
// a single goroutine crash does not bring down the whole process.
func RecoveryInterceptor(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "panic recovered in gRPC handler",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(ctx, req)
}

// RecoveryStreamInterceptor is the stream equivalent of RecoveryInterceptor.
func RecoveryStreamInterceptor(
	srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered in gRPC stream handler",
				"method", info.FullMethod,
				"panic", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(srv, ss)
}

// RequestIDInterceptor generates a UUID request ID for every RPC and stores it
// in the context. Downstream handlers and log lines use RequestIDFromContext.
func RequestIDInterceptor(
	ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	ctx = context.WithValue(ctx, requestIDKey, uuid.New().String())
	return handler(ctx, req)
}

// LoggingInterceptor logs every unary RPC at INFO level with method, peer address,
// duration, and gRPC status code. Use the structured slog output in production.
func LoggingInterceptor(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	code := status.Code(err)

	peerAddr := ""
	if p, ok := peer.FromContext(ctx); ok {
		peerAddr = p.Addr.String()
	}

	slog.InfoContext(ctx, "grpc",
		"method", info.FullMethod,
		"code", code.String(),
		"duration_ms", time.Since(start).Milliseconds(),
		"peer", peerAddr,
		"request_id", RequestIDFromContext(ctx),
	)
	return resp, err
}

// LoggingStreamInterceptor logs stream RPCs at open and close.
func LoggingStreamInterceptor(
	srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	start := time.Now()
	err := handler(srv, ss)
	slog.Info("grpc stream",
		"method", info.FullMethod,
		"code", status.Code(err).String(),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return err
}

// MetricsInterceptor records the standard registry_grpc_request_duration_seconds
// histogram for every unary RPC. The histogram is initialized once on first call
// (after OTEL bootstrap) via initGRPCDurationHist to avoid a meter lookup and
// error allocation on every RPC.
func MetricsInterceptor(
	ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	if hist := initGRPCDurationHist(); hist != nil {
		hist.Record(ctx,
			time.Since(start).Seconds(),
			metric.WithAttributes(
				attribute.String("method", info.FullMethod),
				attribute.String("code", status.Code(err).String()),
			),
		)
	}
	return resp, err
}

// NoopDialOptions returns dial options used in tests to bypass TLS.
// Never use in production — all production gRPC uses mTLS via libs/auth/mtls.
func NoopDialOptions() []grpc.DialOption {
	return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
}
