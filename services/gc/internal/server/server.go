// Package server wires the GC service and starts a cron-based collection loop.
//
// FE-API-032 added a gRPC GCService surface plus persistent gc_runs
// state. The cron loop still ticks on the same interval but it now
// flows through a PersistedRunner so every sweep gets recorded in
// `gc_runs`, and the new RunNow RPC enqueues ad-hoc sweeps that the
// same loop drains between ticks.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/gc/internal/advisory"
	"github.com/steveokay/oci-janus/services/gc/internal/collector"
	"github.com/steveokay/oci-janus/services/gc/internal/config"
	"github.com/steveokay/oci-janus/services/gc/internal/handler"
	"github.com/steveokay/oci-janus/services/gc/internal/repository"
	"github.com/steveokay/oci-janus/services/gc/internal/runner"
	gcmigrations "github.com/steveokay/oci-janus/services/gc/migrations"
)

// Run initialises all dependencies, starts the GC cron loop, and serves
// gRPC + health + metrics endpoints.
func Run(ctx context.Context, cfg *config.Config) error {
	// SEC-038 — Per-target mTLS dial options with the remote service's
	// SAN/CN pinned in serverName, plus fail-closed on TLS load error.
	// The legacy `clientCreds(cfg)` helper passed an empty serverName
	// for every dial (no CN/SAN pin → MITM/impersonation could pin gc
	// to any tenant for the process lifetime) and silently fell back
	// to plaintext on cert-load failure. Every other Phase 3.4 service
	// builds its dial creds via libs/auth/mtls.ClientCreds with the
	// remote name passed in; gc now matches.
	metaCreds, err := cfg.MTLSClientCreds("registry-metadata")
	if err != nil {
		return fmt.Errorf("build metadata gRPC creds: %w", err)
	}
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(metaCreds))
	if err != nil {
		return fmt.Errorf("dial metadata: %w", err)
	}
	defer metaConn.Close()

	storageCreds, err := cfg.MTLSClientCreds("registry-storage")
	if err != nil {
		return fmt.Errorf("build storage gRPC creds: %w", err)
	}
	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpc.WithTransportCredentials(storageCreds))
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	// Tenant directory dial — mandatory (REDESIGN-001 Phase 3.4 / 9.3 reuses
	// this conn for the bootstrap-tenant-id fetch wired into the gRPC server
	// interceptor below). The platform is single-tenant (ADR-0031), so
	// TENANT_GRPC_ADDR is required; see the fail-loud check below.
	var (
		tenantConn        *grpc.ClientConn
		bootstrapTenantID string
	)
	if cfg.TenantGRPCAddr != "" {
		tenantCreds, err := cfg.MTLSClientCreds("registry-tenant")
		if err != nil {
			return fmt.Errorf("build tenant gRPC creds: %w", err)
		}
		tenantConn, err = grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(tenantCreds))
		if err != nil {
			return fmt.Errorf("dial tenant: %w", err)
		}
		defer tenantConn.Close()
	}
	// REDESIGN-001 Phase 3.4 / 9.3 — fetch the bootstrap tenant once at boot
	// and pin every inbound RPC to it via SingleTenantInjector. The fetch
	// reuses tenantConn so the gc service does not open a second TCP stream
	// to registry-tenant. The platform is single-tenant (ADR-0031), so this
	// is unconditional; fail-loud on missing TENANT_GRPC_ADDR because the
	// alternative — silently routing every sweep to the wrong tenant — is far
	// worse than a clean startup error.
	if tenantConn == nil {
		return fmt.Errorf("TENANT_GRPC_ADDR is required (Phase 3.4)")
	}
	bootstrapTenantID, err = tenantbootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
	if err != nil {
		return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
	}
	slog.Info("single-tenant injector wired",
		"bootstrap_tenant_id", bootstrapTenantID,
		"tenant_grpc", cfg.TenantGRPCAddr)

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
	).WithTenantClient(tenantConn) // no-op when tenantConn is nil

	// FE-API-032: optional persistence pool + repository. When DB_DSN
	// is unset the gRPC service still starts but every RPC returns
	// FailedPrecondition. This keeps backwards compatibility with the
	// pre-FE-API-032 deployment model where gc was purely cron-driven.
	var (
		dbPool      *pgxpool.Pool
		repo        *repository.Repository
		runRequests chan uuid.UUID
		persisted   *runner.PersistedRunner
	)
	if cfg.DBDSN != "" {
		tmpDB := &loader.DBConfig{
			DBDSN:       cfg.DBDSN,
			DBMaxConns:  cfg.DBMaxConns,
			Environment: cfg.OTELEnvironment,
		}
		poolCfg, err := tmpDB.PoolConfig()
		if err != nil {
			return fmt.Errorf("build pool config: %w", err)
		}
		dbPool, err = pgxpool.NewWithConfig(ctx, poolCfg)
		if err != nil {
			return fmt.Errorf("pgxpool.New: %w", err)
		}
		defer dbPool.Close()

		if err := runMigrations(ctx, cfg.DBDSN); err != nil {
			return fmt.Errorf("run gc migrations: %w", err)
		}
		repo = repository.New(dbPool)
		// Buffered channel — the dispatcher drains queued rows in
		// bursts, so a single buffered slot is enough to debounce
		// rapid back-to-back RunNow calls without dropping the hint.
		runRequests = make(chan uuid.UUID, 16)
		persisted = runner.New(col, repo, cfg.GCMode)
		slog.Info("gc persistence enabled — sweeps will be recorded in gc_runs")
	} else {
		slog.Warn("DB_DSN not set — gc sweeps will not be persisted; GCService gRPC surface disabled")
	}

	// FE-API-040: wire the retention executor. We attach the metadata gRPC
	// stub so the dispatcher's retention / retention_grace branches have a
	// way to call MarkPending / DeleteManifest / etc. The grace ticker fires
	// every cfg.RetentionGraceIntervalHours; setting it to 0 disables the
	// automatic finaliser sweep (operator can still trigger via the gRPC).
	//
	// FE-API-041: hand the same publisher used by the collector to the
	// retention executor so retention.evaluated / .applied / .grace_completed
	// events flow through one connection. WithPublisher accepts nil, so a
	// future deployment without a broker still gets a working executor.
	if persisted != nil {
		persisted = persisted.
			WithMetadataClient(metadatav1.NewMetadataServiceClient(metaConn)).
			WithPublisher(pub)
		persisted.SetRetentionConfig(runner.RetentionConfig{
			GraceWindow: time.Duration(cfg.RetentionGraceDays) * 24 * time.Hour,
		})
	}

	// Cron loop: persisted path goes through runner so every sweep
	// records a gc_runs row; the legacy path keeps the old in-memory
	// behaviour for deployments that haven't enabled DB_DSN yet.
	interval := time.Duration(cfg.GCRunIntervalHours) * time.Hour
	graceInterval := time.Duration(cfg.RetentionGraceIntervalHours) * time.Hour
	if persisted != nil {
		go persisted.CronLoop(ctx, interval, graceInterval, runRequests)
	} else {
		go runLoop(ctx, col, interval)
	}

	singleTenantInterceptor := grpcmw.SingleTenantInjector(bootstrapTenantID)
	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	// Register the GCService handler. When persistence is disabled we
	// register with a nil repository so every RPC returns
	// FailedPrecondition rather than crashing. The schedule hint is
	// derived from the configured cron interval.
	var grpcRepo handler.Repository
	if repo != nil {
		grpcRepo = repo
	}
	var dispatchCh chan<- uuid.UUID
	if runRequests != nil {
		dispatchCh = runRequests
	}
	gcv1.RegisterGCServiceServer(grpcSrv, handler.New(grpcRepo, dispatchCh, interval, int32(cfg.RetentionGraceDays)))

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
//
// extraUnary, when non-nil, is chained after libs/middleware/grpc.ServerInterceptors
// so the SingleTenantInjector (REDESIGN-001 Phase 3.4) runs after auth/tenant
// extraction but before reaching handlers.
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

// runLoop is the legacy non-persisted cron loop kept for deployments
// where DB_DSN is not set. Runs one GC pass immediately, then repeats
// every interval until ctx is cancelled.
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

// runMigrations applies the gc service's SQL migrations to the DB at
// startup. Mirrors the pattern used by the scanner service in
// FE-API-018: goose with embed.FS, distinct DB pool because goose
// expects a database/sql driver.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(gcmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
