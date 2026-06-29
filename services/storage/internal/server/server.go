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
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
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
	//
	// REDESIGN-001 Phase 3.4 — Single-tenant injector wiring. In
	// DEPLOYMENT_MODE=single we look up the bootstrap tenant id from
	// services/tenant.deployment_metadata at startup so the server-side
	// interceptor can reject mismatched x-tenant-id metadata. Fail-loud:
	// if the lookup errors, storage exits. In multi mode the dial is skipped.
	var singleTenantInterceptor grpc.UnaryServerInterceptor
	if cfg.DeploymentMode == loader.DeploymentModeSingle {
		bootstrapTenantID, err := fetchBootstrapTenantID(ctx, cfg)
		if err != nil {
			return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
		}
		singleTenantInterceptor = grpcmw.SingleTenantInjector(bootstrapTenantID)
		slog.Info("single-mode tenant injector wired",
			"bootstrap_tenant_id", bootstrapTenantID,
			"tenant_grpc", cfg.TenantGRPCAddr,
		)
	}

	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
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

// buildGRPCOptions assembles server options. extraUnary is the optional
// Phase 3.4 SingleTenantInjector (nil in multi mode).
func buildGRPCOptions(cfg *config.Config, extraUnary grpc.UnaryServerInterceptor) ([]grpc.ServerOption, error) {
	chain := grpcmw.ServerInterceptors()
	if extraUnary != nil {
		chain = append(chain, extraUnary)
	}
	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(chain...),
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

// fetchBootstrapTenantID dials services/tenant and delegates the post-dial
// RPC + parse to libs/tenant/bootstrap.FetchTenantID. Caller in Run() converts
// any error to a fatal startup failure (REDESIGN-001 Phase 3.4).
func fetchBootstrapTenantID(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.TenantGRPCAddr == "" {
		return "", fmt.Errorf("TENANT_GRPC_ADDR is required when DEPLOYMENT_MODE=single (Phase 3.4)")
	}
	tenantCreds, err := mtls.ClientCreds(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, "registry-tenant")
	if err != nil {
		return "", fmt.Errorf("build tenant gRPC creds: %w", err)
	}
	tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(tenantCreds))
	if err != nil {
		return "", fmt.Errorf("dial tenant gRPC: %w", err)
	}
	defer tenantConn.Close()
	return tenantbootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics.Handler().ServeHTTP(w, r)
}
