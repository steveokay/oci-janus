// Package server wires the GC service and starts a cron-based collection loop.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/gc/internal/advisory"
	"github.com/steveokay/oci-janus/services/gc/internal/collector"
	"github.com/steveokay/oci-janus/services/gc/internal/config"
)

// Run initialises all dependencies, starts the GC cron loop, and serves health/metrics.
func Run(ctx context.Context, cfg *config.Config) error {
	creds := clientCreds(cfg)
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, creds)
	if err != nil {
		return fmt.Errorf("dial metadata: %w", err)
	}
	defer metaConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, creds)
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	var locker *advisory.Locker
	if cfg.GCAdvisoryLockDBDSN != "" {
		pool, err := pgxpool.New(ctx, cfg.GCAdvisoryLockDBDSN)
		if err != nil {
			return fmt.Errorf("advisory lock pool: %w", err)
		}
		defer pool.Close()
		locker = advisory.New(pool)
		slog.Info("gc advisory locking enabled")
	} else {
		slog.Warn("GC_ADVISORY_LOCK_DB_DSN not set — advisory locking disabled (safe for single-worker only)")
	}

	col := collector.New(
		metaConn, storageConn, pub,
		locker,
		cfg.GCMode,
		cfg.BlobMinAgeHours,
		cfg.ManifestMinAgeHours,
	)

	// Start GC loop — runs immediately then every GCRunIntervalHours.
	go runLoop(ctx, col, time.Duration(cfg.GCRunIntervalHours)*time.Hour)

	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// ReadHeaderTimeout prevents Slowloris attacks.
	// ReadTimeout and WriteTimeout bound the full request/response cycle.
	// SecureHeaders adds X-Content-Type-Options, X-Frame-Options to every response.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(httpMux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the GC health port to the cluster.
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

	errCh := make(chan error, 3)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("metrics server starting", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down gc service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// buildGRPCOptions returns server options with interceptors and optional mTLS.
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

// clientCreds returns a dial option with mTLS when cert paths are configured,
// falling back to insecure with a warning for development without certs.
func clientCreds(cfg *config.Config) grpc.DialOption {
	if cfg.MTLSCACertPath != "" && cfg.MTLSCertPath != "" && cfg.MTLSKeyPath != "" {
		tlsCfg, err := mtls.ClientTLSConfig(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, "")
		if err != nil {
			slog.Warn("mTLS client config failed, falling back to insecure", "error", err)
			return grpc.WithTransportCredentials(insecure.NewCredentials())
		}
		return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
	}
	slog.Warn("mTLS not configured — gRPC client using insecure (development mode only)")
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// runLoop runs one GC pass immediately, then repeats every interval until ctx is cancelled.
func runLoop(ctx context.Context, col *collector.Collector, interval time.Duration) {
	run := func() {
		res, err := col.Run(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "gc run failed", "error", err)
			return
		}
		slog.InfoContext(ctx, "gc run finished",
			"manifests_deleted", res.ManifestsDeleted,
			"blobs_deleted", res.BlobsDeleted,
			"bytes_freed", res.BytesFreed,
		)
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
