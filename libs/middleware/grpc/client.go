package grpc

import (
	"context"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultCallTimeout is applied to every outgoing RPC when the caller has not
// already set a deadline. Keeps slow upstream services from blocking indefinitely.
const defaultCallTimeout = 5 * time.Second

// ClientInterceptors returns the ordered unary interceptor chain for gRPC clients.
// Apply via grpc.WithChainUnaryInterceptor when constructing a client connection.
// OTEL tracing is handled separately — pass OTELClientHandler() as a DialOption.
func ClientInterceptors() []grpc.UnaryClientInterceptor {
	return []grpc.UnaryClientInterceptor{
		DeadlineInterceptor,
		RetryInterceptor,
	}
}

// OTELClientHandler returns a grpc.DialOption that installs the OpenTelemetry
// stats handler on the client connection. Use alongside WithChainUnaryInterceptor.
func OTELClientHandler() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithTracerProvider(otel.GetTracerProvider()),
		otelgrpc.WithMeterProvider(otel.GetMeterProvider()),
	))
}

// DeadlineInterceptor attaches a deadline to any RPC context that does not
// already have one. This prevents unbounded blocks when a downstream service
// hangs — callers should prefer passing their own derived context with a tighter
// timeout when the endpoint-specific SLA is known.
func DeadlineInterceptor(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// RetryInterceptor retries transient failures up to maxRetries times with
// exponential back-off. Only UNAVAILABLE and DEADLINE_EXCEEDED are retried —
// codes.ResourceExhausted is NOT retried because the pool is genuinely exhausted
// and retrying immediately would make the situation worse (REM-006).
func RetryInterceptor(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	const maxRetries = 3
	backoff := 100 * time.Millisecond

	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = invoker(ctx, method, req, reply, cc, opts...)
		if err == nil {
			return nil
		}

		code := status.Code(err)
		if code != codes.Unavailable && code != codes.DeadlineExceeded {
			// Non-retryable error — return immediately
			return err
		}
		if attempt == maxRetries {
			break
		}
		// Check context before sleeping so we don't delay a cancelled caller
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return err
}
