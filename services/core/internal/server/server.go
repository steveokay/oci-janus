// Package server wires all dependencies and runs the gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/core/internal/config"
	"github.com/steveokay/oci-janus/services/core/internal/handler"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

// Run wires everything and blocks until ctx is cancelled or a server errors.
func Run(ctx context.Context, cfg *config.Config) error {
	// Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	defer rdb.Close()

	// gRPC client transport: mTLS when cert paths are configured, plaintext otherwise.
	grpcCreds, err := clientCreds(cfg)
	if err != nil {
		return fmt.Errorf("load mTLS creds: %w", err)
	}

	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpcCreds)
	if err != nil {
		return fmt.Errorf("dial auth: %w", err)
	}
	defer authConn.Close()

	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpcCreds)
	if err != nil {
		return fmt.Errorf("dial metadata: %w", err)
	}
	defer metaConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpcCreds)
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	// Trigger eager connection establishment so the first inbound request doesn't
	// stall while the gRPC TCP/HTTP-2 handshake completes.
	authConn.Connect()
	metaConn.Connect()
	storageConn.Connect()

	// RabbitMQ publisher
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	// Service layer
	uploadStore := service.NewUploadStore(rdb)
	referrerStore := service.NewReferrerStore(rdb)
	authClient := service.NewAuthClient(authConn, rdb)
	// FE-API-042: PullEventSampleRate gates the pull.image publish on every
	// successful manifest GET. Validated at startup to be in [0.0, 1.0].
	registry := service.NewRegistry(metaConn, storageConn, uploadStore, referrerStore, pub, cfg.PullEventSampleRate)

	// HTTP handler
	h := handler.New(authClient, registry, cfg.AuthRealm)
	mux := http.NewServeMux()
	h.Register(mux)

	// otelhttp wraps the handler tree to create a root span for every HTTP request.
	// SecureHeaders is inside so security headers appear on all responses including errors.
	// ReadTimeout and WriteTimeout are generous to accommodate blob streaming.
	// ReadHeaderTimeout is short to prevent Slowloris attacks.
	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: otelhttp.NewHandler(
			httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 1<<30)),
			"registry-core",
		),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // 60s to allow large blob layer transfers to complete
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the OCI business port to the cluster.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics.Handler().ServeHTTP(w, r)
	})
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// gRPC server (health check only for now; future: expose internal gRPC if needed).
	// SEC-010: use standard interceptors (recovery, OTEL tracing, logging) so panics
	// are caught and observability is consistent with other services.
	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthpb.RegisterHealthServer(grpcSrv, health.NewServer())

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	errCh := make(chan error, 3)
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
	}()
	go func() {
		slog.Info("metrics server starting", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// buildGRPCOptions returns server options for the health-check gRPC server.
// Even though this server only exposes the health protocol, we attach the
// standard interceptor chain (panic recovery, OTEL tracing, structured logging)
// so that the health endpoint is observable and cannot panic-crash the process.
// mTLS is applied when cert paths are configured (SEC-010).
func buildGRPCOptions(cfg *config.Config) ([]grpc.ServerOption, error) {
	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(grpcmw.ServerInterceptors()...),
		grpc.ChainStreamInterceptor(grpcmw.StreamServerInterceptors()...),
	}

	if cfg.MTLSCACertPath != "" && cfg.MTLSCertPath != "" && cfg.MTLSKeyPath != "" {
		tlsCfg, err := mtls.ServerTLSConfig(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load mTLS certs: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		slog.Warn("mTLS not configured — gRPC running without TLS (development mode only)")
	}

	return opts, nil
}

// clientCreds returns mTLS dial credentials when all three cert paths are set,
// falling back to plaintext for local dev without certs.
func clientCreds(cfg *config.Config) (grpc.DialOption, error) {
	if cfg.MTLSCACertPath != "" && cfg.MTLSCertPath != "" && cfg.MTLSKeyPath != "" {
		tlsCfg, err := mtls.ClientTLSConfig(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, "")
		if err != nil {
			return nil, err
		}
		return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
	}
	slog.Warn("mTLS not configured — gRPC clients running without TLS (development mode only)")
	return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
}
