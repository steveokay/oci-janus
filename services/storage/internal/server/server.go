// Package server wires the gRPC and HTTP servers together and manages graceful shutdown.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/storage/internal/config"
	"github.com/steveokay/oci-janus/services/storage/internal/driver"
	"github.com/steveokay/oci-janus/services/storage/internal/handler"
)

// Run starts the gRPC and HTTP servers and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// ── 1. Storage driver ─────────────────────────────────────────────────────
	drv, err := initDriver(cfg)
	if err != nil {
		return fmt.Errorf("init storage driver: %w", err)
	}

	if err := drv.Ping(ctx); err != nil {
		return fmt.Errorf("storage backend ping failed: %w", err)
	}
	slog.Info("storage driver ready", "driver", cfg.StorageDriver)

	// ── 2. gRPC server ────────────────────────────────────────────────────────
	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)

	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	storagev1.RegisterStorageServiceServer(grpcSrv, handler.New(drv))
	healthSrv.SetServingStatus("registry.storage.v1.StorageService", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	// ── 3. HTTP server ────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// ReadHeaderTimeout prevents Slowloris attacks.
	// WriteTimeout is generous (60s) because this service proxies large blob
	// streams between storage backends and gRPC clients.
	// SecureHeaders is outermost so security headers appear on all responses.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 4<<20)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // 60s for blob streaming responses
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the storage HTTP port to the cluster.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", metricsHandler)
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// ── 4. Start & block ──────────────────────────────────────────────────────
	errCh := make(chan error, 3)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- fmt.Errorf("gRPC serve: %w", err)
		}
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP serve: %w", err)
		}
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

func initDriver(cfg *config.Config) (driver.Driver, error) {
	switch cfg.StorageDriver {
	case "minio":
		return driver.NewMinIO(
			cfg.StorageMinIOEndpoint,
			cfg.StorageMinIOAccessKey,
			cfg.StorageMinIOSecretKey,
			cfg.StorageMinIOBucket,
			cfg.StorageMinIORegion,
			cfg.StorageMinIOUseSSL,
		)
	case "filesystem":
		return driver.NewFilesystem(cfg.StorageFilesystemRoot)
	default:
		return nil, fmt.Errorf("storage driver %q not implemented; supported: minio, filesystem", cfg.StorageDriver)
	}
}

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

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics.Handler().ServeHTTP(w, r)
}
