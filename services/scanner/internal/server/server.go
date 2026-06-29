// Package server wires all scanner dependencies and starts gRPC + HTTP servers.
package server

import (
	"context"
	"errors"
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
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/auth/mtls"

	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/config"
	"github.com/steveokay/oci-janus/services/scanner/internal/handler"
	internalPlugin "github.com/steveokay/oci-janus/services/scanner/internal/plugin"
	"github.com/steveokay/oci-janus/services/scanner/internal/policy"
	scannerregistry "github.com/steveokay/oci-janus/services/scanner/internal/registry"
	"github.com/steveokay/oci-janus/services/scanner/internal/reportworker"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"
	scannermigrations "github.com/steveokay/oci-janus/services/scanner/migrations"

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

	// SEC-039: per-target dial creds with serverName pinned to each remote's
	// expected CN/SAN. A shared `creds` value with empty serverName would
	// skip the CN/SAN check on each downstream dial.
	metaCreds, err := clientCreds(cfg, "registry-metadata")
	if err != nil {
		return fmt.Errorf("build metadata mTLS creds: %w", err)
	}
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, metaCreds)
	if err != nil {
		return fmt.Errorf("dial metadata %s: %w", cfg.MetadataGRPCAddr, err)
	}
	defer metaConn.Close()

	storageCreds, err := clientCreds(cfg, "registry-storage")
	if err != nil {
		return fmt.Errorf("build storage mTLS creds: %w", err)
	}
	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, storageCreds)
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

	// FUT-017: consumer for cache.populated events (auto-scan freshly
	// proxied images per the per-upstream policy).
	cachePopulatedCons, err := consumer.New(cfg.RabbitMQURL, worker.CachePopulatedConsumerConfig())
	if err != nil {
		return fmt.Errorf("rabbitmq cache.populated consumer: %w", err)
	}
	defer cachePopulatedCons.Close()

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

	// FUT-017: per-upstream proxy-cache scan policy resolver. Looks up
	// proxy_cache_scan_policies by (tenant_id, upstream_name). ErrNotFound
	// surfaces as (false, false, nil) so the consumer treats "no row" as
	// "operator never opted in" and ACKs the event without scanning.
	// Genuine DB errors propagate so the consumer can NACK and fail
	// closed — we'd rather have a few redeliveries than silently flip
	// auto-scan on or off on the back of a transient blip.
	pool.WithProxyCachePolicyResolver(func(ctx context.Context, tenantIDStr, upstreamName string) (worker.ProxyCachePolicy, bool, error) {
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			return worker.ProxyCachePolicy{}, false, fmt.Errorf("invalid tenant_id: %w", err)
		}
		rec, err := repo.GetProxyCacheScanPolicy(ctx, tenantID, upstreamName)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return worker.ProxyCachePolicy{}, false, nil
			}
			return worker.ProxyCachePolicy{}, false, err
		}
		// Map the wire-level severity_threshold ("", "none", "low",
		// "medium", "high", "critical") into the uppercase form the
		// worker's hasPolicyViolation expects. "" and "none" both mean
		// "never block", which is also the worker's empty-string
		// convention.
		threshold := ""
		switch rec.SeverityThreshold {
		case "low":
			threshold = "LOW"
		case "medium":
			threshold = "MEDIUM"
		case "high":
			threshold = "HIGH"
		case "critical":
			threshold = "CRITICAL"
		}
		return worker.ProxyCachePolicy{
			AutoScan:          rec.AutoScan,
			SeverityThreshold: threshold,
		}, true, nil
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

	// Consume cache.populated events for FUT-017 auto-scan-on-proxied-image.
	go func() {
		slog.Info("starting cache.populated consumer")
		if err := cachePopulatedCons.Consume(ctx, pool.HandleCachePopulated); err != nil {
			slog.Error("cache.populated consumer stopped", "error", err)
		}
	}()

	// gRPC server — mTLS required when cert paths are configured (per
	// CLAUDE.md §7). Falls back to plaintext with a WARN only when the
	// operator left the cert paths empty (dev only). REM-011 Phase 2
	// added a real internal caller (services/management) for this gRPC
	// surface, so plaintext silently breaks the admin scanner routes
	// with a "first record does not look like a TLS handshake" error
	// at the management → scanner edge — make TLS the default.
	// REDESIGN-001 Phase 3.4 — Single-tenant injector wiring. In
	// DEPLOYMENT_MODE=single scanner dials services/tenant at startup,
	// fetches bootstrap_tenant_id, and wires SingleTenantInjector into
	// its gRPC unary chain. Fail-loud on lookup error; multi mode skips.
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

	// RED-FU-013 — build the gRPC server options via a helper so the
	// chain shape matches every other Phase 3.4 service and can be
	// unit-tested (services/scanner/internal/server/build_grpc_options_test.go).
	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
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

// buildGRPCOptions returns server options for the scanner gRPC server,
// including the standard interceptor chain (recovery / OTEL / logging),
// an optional Phase 3.4 SingleTenantInjector slot, and mTLS server creds
// when cert paths are configured.
//
// RED-FU-013 extracted this from inline `Run()` so the shape matches the
// other 10 Phase 3.4 services and can be smoke-tested
// (build_grpc_options_test.go). Pre-Phase 3.4 scanner had NO interceptors
// at all (closed by PR #175 alongside the SingleTenantInjector wiring);
// keeping the chain encapsulated here makes that regression structurally
// hard to re-introduce.
//
// extraUnary, when non-nil, is chained after libs/middleware/grpc.ServerInterceptors
// so the SingleTenantInjector sees a fully-decorated context.
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

// clientCreds returns mTLS dial credentials with serverName pinned to the
// remote service's expected CN/SAN (e.g. "registry-metadata", "registry-storage"),
// falling back to plaintext insecure for local dev without certs. SEC-039:
// the previous signature passed an empty serverName so no per-target
// CN/SAN pin was enforced. mtls.ClientCreds returns insecure.NewCredentials
// only when ALL cert paths are empty (dev posture); with paths set it
// returns the error on TLS load failure so a corrupted cert fails-loud
// instead of silently downgrading to plaintext.
func clientCreds(cfg *config.Config, serverName string) (grpc.DialOption, error) {
	creds, err := mtls.ClientCreds(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, serverName)
	if err != nil {
		return nil, err
	}
	if cfg.MTLSCACertPath == "" || cfg.MTLSCertPath == "" || cfg.MTLSKeyPath == "" {
		slog.Warn("mTLS not configured — scanner gRPC clients running without TLS (development mode only)")
	}
	return grpc.WithTransportCredentials(creds), nil
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

// fetchBootstrapTenantID dials services/tenant and delegates the post-dial
// RPC + parse to libs/tenant/bootstrap.FetchTenantID (REDESIGN-001 Phase 3.4).
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
