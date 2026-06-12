// Package otel bootstraps the OpenTelemetry tracer and meter providers.
// Call Bootstrap once in main.go before starting any server, and call the
// returned shutdown function on process exit to flush pending spans/metrics.
package otel

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds the OTEL exporter configuration, typically populated from BaseConfig.
type Config struct {
	// Exporter selects the backend: "jaeger", "tempo", "datadog", or "stdout"
	Exporter string
	// Endpoint is the OTLP collector address (e.g. "localhost:4317")
	Endpoint    string
	ServiceName string
	Environment string
	// SamplingRate between 0.0 and 1.0; use 1.0 in dev, lower in high-traffic prod
	SamplingRate float64
}

// Bootstrap initialises the global OTEL tracer and meter providers and sets
// the W3C TraceContext + Baggage propagators. Returns a shutdown function that
// must be called on process exit to flush all pending telemetry.
func Bootstrap(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	res, err := buildResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	traceExp, err := newTraceExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SamplingRate))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	metricExp, err := newMetricExporter(ctx, cfg)
	if err != nil {
		// Shut down the already-started trace provider before returning
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	return func(shutdownCtx context.Context) error {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return mp.Shutdown(shutdownCtx)
	}, nil
}

// buildResource creates the OTEL resource describing this service instance.
// NewSchemaless avoids a schema-URL conflict between resource.Default() (SDK v1.43
// embeds semconv v1.40) and any older semconv package imported elsewhere.
func buildResource(cfg Config) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(cfg.ServiceName),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
}

// otelInsecure reads the OTEL_INSECURE environment variable. When false (the
// production default), the OTLP exporter connects over TLS. Set OTEL_INSECURE=true
// only in local docker-compose environments where Jaeger/Tempo run without a
// server certificate on the collector endpoint.
//
// Never set OTEL_INSECURE=true in staging or production — doing so sends all
// trace and metric data in cleartext.
func otelInsecure() bool {
	return strings.EqualFold(os.Getenv("OTEL_INSECURE"), "true")
}

// newTraceExporter returns a span exporter for the configured backend.
// "stdout" is intended for local development only — it writes JSON to os.Stdout.
func newTraceExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	case "jaeger", "tempo", "datadog":
		// All three use OTLP/gRPC — differentiated only by the Endpoint env var.
		// Build option list dynamically so TLS is the default; insecure mode
		// is opt-in via OTEL_INSECURE=true for local dev stacks.
		traceOpts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
		}
		if otelInsecure() {
			// Only safe for local dev with no TLS on the collector endpoint.
			traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, traceOpts...)
	default:
		return nil, fmt.Errorf("unknown OTEL_EXPORTER %q; want jaeger|tempo|datadog|stdout", cfg.Exporter)
	}
}

// newMetricExporter returns a metric exporter for the configured backend.
// As with newTraceExporter, the OTLP/gRPC connection uses TLS by default and
// is downgraded to plaintext only when OTEL_INSECURE=true is explicitly set.
func newMetricExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	switch cfg.Exporter {
	case "stdout":
		return stdoutmetric.New()
	case "jaeger", "tempo", "datadog":
		// Build option list dynamically so TLS is used unless OTEL_INSECURE=true.
		metricOpts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		}
		if otelInsecure() {
			// Only safe for local dev with no TLS on the collector endpoint.
			metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
		}
		return otlpmetricgrpc.New(ctx, metricOpts...)
	default:
		return nil, fmt.Errorf("unknown OTEL_EXPORTER %q; want jaeger|tempo|datadog|stdout", cfg.Exporter)
	}
}
