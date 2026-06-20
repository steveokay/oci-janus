// Package server wires all scanner dependencies and starts gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/scanner/internal/config"
	"github.com/steveokay/oci-janus/services/scanner/internal/handler"
	scannermigrations "github.com/steveokay/oci-janus/services/scanner/migrations"
	internalPlugin "github.com/steveokay/oci-janus/services/scanner/internal/plugin"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
	"github.com/steveokay/oci-janus/services/scanner/internal/reportworker"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
)

// Run starts all service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Validate and load the scanner plugin binary (checksum verified here — service
	// refuses to start if the binary has been tampered with).
	scannerPlugin, err := internalPlugin.New(cfg.PluginPath, cfg.PluginChecksum)
	if err != nil {
		return fmt.Errorf("load scanner plugin: %w", err)
	}

	creds, err := clientCreds(cfg)
	if err != nil {
		return fmt.Errorf("build mTLS creds: %w", err)
	}

	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, creds)
	if err != nil {
		return fmt.Errorf("dial metadata %s: %w", cfg.MetadataGRPCAddr, err)
	}
	defer metaConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, creds)
	if err != nil {
		return fmt.Errorf("dial storage %s: %w", cfg.StorageGRPCAddr, err)
	}
	defer storageConn.Close()

	// FE-API-018/019 — scanner now owns a Postgres schema. PoolConfig
	// rejects sslmode=disable in production and applies the platform's
	// pool tuning defaults.
	tmpDB := &loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns, Environment: cfg.OTELEnvironment}
	poolCfg, err := tmpDB.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}
	dbPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer dbPool.Close()

	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	repo := repository.New(dbPool)

	// RabbitMQ publisher for scan.completed / scan.policy_blocked events.
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	// Consumer for push.completed events (automatic scan on every image push).
	cons, err := consumer.New(cfg.RabbitMQURL, worker.ConsumerConfig())
	if err != nil {
		return fmt.Errorf("rabbitmq consumer: %w", err)
	}
	defer cons.Close()

	// Consumer for scan.queued events (manually triggered scans via management API).
	scanQueuedCons, err := consumer.New(cfg.RabbitMQURL, worker.ScanQueuedConsumerConfig())
	if err != nil {
		return fmt.Errorf("rabbitmq scan.queued consumer: %w", err)
	}
	defer scanQueuedCons.Close()

	scanStore := store.New()
	pool := worker.NewPool(
		scannerPlugin,
		metaConn,
		storageConn,
		pub,
		scanStore,
		cfg.WorkerCount,
		time.Duration(cfg.JobTimeoutSecs)*time.Second,
	)

	// Start worker pool goroutines — they block on the jobs channel until ctx is cancelled.
	go pool.Start(ctx, cfg.WorkerCount)

	// Compliance-report worker: polls compliance_reports and renders PDF +
	// SPDX output. Safe to run alongside multiple replicas via
	// FOR UPDATE SKIP LOCKED inside ClaimPendingReport.
	rw := reportworker.New(repo, reportworker.Config{
		OutputDir:    cfg.ReportOutputDir,
		PollInterval: time.Duration(cfg.ReportPollIntervalSecs) * time.Second,
	})
	go rw.Run(ctx)

	// Consume push.completed events and dispatch to the worker pool.
	go func() {
		slog.Info("starting push.completed consumer")
		if err := cons.Consume(ctx, pool.HandlePushCompleted); err != nil {
			slog.Error("push.completed consumer stopped", "error", err)
		}
	}()

	// Consume scan.queued events for manually triggered scans (from management API).
	go func() {
		slog.Info("starting scan.queued consumer")
		if err := scanQueuedCons.Consume(ctx, pool.HandleScanQueued); err != nil {
			slog.Error("scan.queued consumer stopped", "error", err)
		}
	}()

	// gRPC server.
	grpcSrv := grpc.NewServer()
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	scannerv1.RegisterScannerServiceServer(grpcSrv, handler.New(pool, scanStore).WithRepository(repo))
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
	// to scrape :9090 without exposing the scanner service ports to the cluster.
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
		slog.Info("shutting down scanner")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
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
	slog.Warn("mTLS not configured — scanner gRPC clients running without TLS (development mode only)")
	return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
}

// runMigrations runs goose SQL migrations against the scanner DB.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(scannermigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
