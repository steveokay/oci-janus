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
	"github.com/google/uuid"

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
	"github.com/steveokay/oci-janus/services/scanner/internal/policy"
	scannerregistry "github.com/steveokay/oci-janus/services/scanner/internal/registry"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
	"github.com/steveokay/oci-janus/services/scanner/internal/reportworker"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
)

// Run starts all service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Discover adapter binaries on disk (REM-011 Phase 2). The registry
	// is the source of truth for what SetActiveAdapter is allowed to
	// switch to. Allowlist + prefixes mirror plugin.pluginEnv() so the
	// UI's env_keys field matches what the plugin process will actually
	// see at scan time.
	adapterReg, err := scannerregistry.New(scannerregistry.Options{
		EnvAllowlist: []string{
			"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
			"USER", "USERNAME",
			"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		},
		EnvPrefixes: []string{"TRIVY_", "GRYPE_"},
	})
	if err != nil {
		return fmt.Errorf("scanner adapter registry: %w", err)
	}

	// Boot-time scanner plugin construction is deferred until after
	// `selectInitialAdapter` resolves the persisted choice in
	// scanner_settings (REM-011 Phase 2). Otherwise the worker pool would
	// always boot with cfg.PluginPath (env-var default = dev-stub in
	// dev), even when the operator had swapped to trivy via the admin
	// UI — restarting the scanner silently reverted the swap. We still
	// need this declared up front so the rest of the boot flow can reuse
	// it; the *ProcessPlugin satisfies the plugin.Scanner contract the
	// worker.NewPool expects.
	var scannerPlugin *internalPlugin.ProcessPlugin

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

	// Seed the registry's active selection. Order is:
	//   1. scanner_settings row (operator's last SetActiveAdapter choice).
	//   2. SCANNER_PLUGIN_PATH env var (Phase 1 behaviour preserved).
	// Either path is validated against the registry — an unknown path
	// (binary removed since last swap) logs a warning and falls back to
	// the env-var default. If even that is unknown, the service still
	// starts; SetActiveAdapter can bootstrap the selection at runtime.
	if err := selectInitialAdapter(ctx, adapterReg, repo, cfg.PluginPath); err != nil {
		slog.WarnContext(ctx, "selecting initial scanner adapter — using none", "err", err)
	}

	// REM-013 follow-up — boot the worker pool with whatever
	// `selectInitialAdapter` chose. Without this, the pool was always
	// constructed from cfg.PluginPath (env-var default = dev-stub),
	// silently ignoring the operator's persisted swap on every restart.
	// We re-derive the active adapter's path + checksum from the
	// registry so the same SHA-256 integrity check that gates the
	// runtime SetActiveAdapter RPC also gates boot.
	if active := adapterReg.Active(); active != nil {
		scannerPlugin, err = internalPlugin.New(active.Path, active.Checksum)
		if err != nil {
			return fmt.Errorf("load active scanner plugin %q: %w", active.Path, err)
		}
		slog.InfoContext(ctx, "scanner adapter boot selection",
			"path", active.Path, "checksum", active.Checksum)
	} else {
		// selectInitialAdapter logs the no-adapter case at WARN; we still
		// need a plugin to construct the pool with so fall back to the
		// env-var path here. The runtime SetActiveAdapter swap can fix
		// the selection without a restart.
		scannerPlugin, err = internalPlugin.New(cfg.PluginPath, cfg.PluginChecksum)
		if err != nil {
			return fmt.Errorf("load fallback scanner plugin: %w", err)
		}
		slog.WarnContext(ctx, "scanner adapter boot selection — no registry entry, using env-var default",
			"path", cfg.PluginPath)
	}

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
	// QA-005: in-memory scan records are useful for live status reads but
	// the metadata service is the system of record. Sweep terminal-status
	// entries hourly so a long-running worker doesn't leak a row per scan.
	go scanStore.StartSweeper(ctx, time.Hour, 24*time.Hour)

	pool := worker.NewPool(
		scannerPlugin,
		metaConn,
		storageConn,
		pub,
		scanStore,
		cfg.WorkerCount,
		time.Duration(cfg.JobTimeoutSecs)*time.Second,
	)
	// Wire the registry's RecordVersion hook so successful scans
	// backfill the per-adapter version cache (otherwise the UI would
	// always show "unknown" until a process restart with a different
	// adapter discovery list).
	pool.SetVersionRecorder(adapterReg)

	// FE-API-049/050: wire the policy resolver so HandlePushCompleted
	// consults the per-repo → org → tenant → default inheritance chain
	// before enqueueing a scan AND so the post-scan worker can honour
	// block_on_severity + exempt_cves when deciding whether to
	// quarantine. The closure parses the UUID strings the push.completed
	// payload carries and delegates to policy.Resolve; any error
	// (invalid UUID, DB blip) returns the "scan everything, quarantine
	// nothing" default so we fail OPEN — losing a scan is worse than
	// scanning against a synthesised default, and silently quarantining
	// against a partial policy view would be worse than not
	// quarantining at all.
	pool.WithPolicyResolver(func(ctx context.Context, tenantIDStr, repoIDStr string) (worker.ResolvedScanPolicy, error) {
		fallback := worker.ResolvedScanPolicy{AutoScanOnPush: true}
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			return fallback, fmt.Errorf("policy resolver: invalid tenant_id: %w", err)
		}
		var repoID uuid.UUID
		if repoIDStr != "" {
			repoID, err = uuid.Parse(repoIDStr)
			if err != nil {
				return fallback, fmt.Errorf("policy resolver: invalid repo_id: %w", err)
			}
		}
		res, err := policy.Resolve(ctx, repo, tenantID, repoID, uuid.Nil)
		if err != nil {
			return fallback, fmt.Errorf("policy resolver: %w", err)
		}
		return worker.ResolvedScanPolicy{
			AutoScanOnPush:  res.Policy.AutoScanOnPush,
			BlockOnSeverity: res.Policy.BlockOnSeverity,
			ExemptCVEs:      res.Policy.ExemptCVEs,
		}, nil
	})

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

	// gRPC server — mTLS required when cert paths are configured (per
	// CLAUDE.md §7). Falls back to plaintext with a WARN only when the
	// operator left the cert paths empty (dev only). REM-011 Phase 2
	// added a real internal caller (services/management) for this gRPC
	// surface, so plaintext silently breaks the admin scanner routes
	// with a "first record does not look like a TLS handshake" error
	// at the management → scanner edge — make TLS the default.
	var grpcOpts []grpc.ServerOption
	if cfg.MTLSCACertPath != "" && cfg.MTLSCertPath != "" && cfg.MTLSKeyPath != "" {
		tlsCfg, err := mtls.ServerTLSConfig(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath)
		if err != nil {
			return fmt.Errorf("load mTLS server certs: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		slog.Warn("scanner gRPC server: mTLS not configured — running plaintext (dev only)")
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	scannerv1.RegisterScannerServiceServer(grpcSrv,
		handler.New(pool, scanStore).
			WithRepository(repo).
			WithAdapterRegistry(adapterReg).
			WithMetadataClient(metadatav1.NewMetadataServiceClient(metaConn)).
			WithTestScanFixture(handler.TestScanFixture{
				TenantID:       cfg.TestScanTenantID,
				RepositoryName: cfg.TestScanRepository,
				ManifestRef:    cfg.TestScanManifestRef,
			}))
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

// selectInitialAdapter resolves the active-adapter selection at boot:
// the persisted choice in scanner_settings wins, then the env-var path.
// Either path is verified against the discovered registry; an unknown
// path falls through to the next candidate so a stale DB pointer (binary
// removed from the image) doesn't brick the service.
func selectInitialAdapter(ctx context.Context, reg *scannerregistry.Registry, repo *repository.Repository, envPath string) error {
	// 1. Persisted choice (operator's last SetActiveAdapter call).
	persistedPath, err := repo.GetActiveAdapter(ctx)
	if err != nil {
		// DB read failed (transient) — fall back to env-var path so the
		// service can still start. The next SetActiveAdapter call will
		// repopulate scanner_settings.
		slog.WarnContext(ctx, "scanner_settings read failed — using env-var adapter", "err", err)
	} else if persistedPath != "" {
		if reg.FindByPath(persistedPath) != nil {
			return reg.SetActive(persistedPath)
		}
		slog.WarnContext(ctx, "persisted adapter not in registry — falling back to env var",
			"persisted_path", persistedPath)
	}

	// 2. Env-var fallback (Phase 1 behaviour).
	if envPath != "" && reg.FindByPath(envPath) != nil {
		return reg.SetActive(envPath)
	}

	// 3. No selection. Service still starts; SetActiveAdapter can bootstrap.
	return fmt.Errorf("no active adapter selected (env path %q not in registry)", envPath)
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
