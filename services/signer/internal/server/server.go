// Package server wires the signer service and starts gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

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
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/config"
	"github.com/steveokay/oci-janus/services/signer/internal/eventconsumer"
	"github.com/steveokay/oci-janus/services/signer/internal/handler"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
	signermigrations "github.com/steveokay/oci-janus/services/signer/migrations"
)

// Run initialises all dependencies and starts the signer service.
func Run(ctx context.Context, cfg *config.Config) error {
	s, err := loadSigner(cfg)
	if err != nil {
		return fmt.Errorf("load signer: %w", err)
	}
	slog.Info("signer loaded", "backend", cfg.SignerKeyBackend, "key_id", s.KeyID())

	// ── Database (optional in dev, required in production) ──────────────────
	// When SIGNER_DB_DSN is set, run goose migrations and open the pool.
	// When absent, log a startup warning and fall back to an in-memory store.
	// The in-memory store loses all records on process restart — this is
	// acceptable for local development but NOT for production (SEC-015).
	var store *sigstore.Store
	// repo is kept around past the var block so the FUT-017 wiring below
	// can pass it to the handler + the cache.populated consumer. Stays nil
	// when SIGNER_DB_DSN is unset — the handler then surfaces a clean
	// FailedPrecondition rather than panicking on the policy RPCs.
	var repo *repository.Repository
	if cfg.DBDSN != "" {
		if err := runMigrations(cfg.DBDSN); err != nil {
			return fmt.Errorf("run signer migrations: %w", err)
		}

		pool, err := openPool(ctx, cfg.DBDSN)
		if err != nil {
			return fmt.Errorf("connect to signer database: %w", err)
		}
		defer pool.Close()

		repo = repository.New(pool)
		store = sigstore.NewWithDB(repo)
		slog.Info("signature store: PostgreSQL persistence enabled")
	} else {
		// SIGNER_DB_DSN not set — fall back to in-memory store.
		// This MUST NOT reach production: records are lost on restart and replicas
		// have independent stores, breaking VerifyManifest for previously-signed images.
		slog.Warn("SIGNER_DB_DSN is not set — using in-memory signature store; " +
			"signature records will be lost on restart (SEC-015: set SIGNER_DB_DSN in production)")
		store = sigstore.New()
	}

	grpcHdl := handler.New(s, store)
	if repo != nil {
		// FUT-017: only wire the proxy-cache policy RPCs when we have a
		// real DB. In-memory mode leaves the RPCs returning
		// FailedPrecondition so an operator who forgot to set
		// SIGNER_DB_DSN gets a legible error rather than a nil-deref panic.
		grpcHdl = grpcHdl.WithProxyCachePolicyRepo(repo)
	}

	// FUT-017: subscribe to cache.populated events when RabbitMQ is wired
	// AND we have a DB to read the policy from. Either missing piece
	// silently disables auto-signing rather than crashing — local dev
	// without RabbitMQ/Postgres stays on the SignManifest-only path the
	// way it always has.
	if cfg.RabbitMQURL != "" && repo != nil {
		cons, err := consumer.New(cfg.RabbitMQURL, eventconsumer.CachePopulatedConsumerConfig())
		if err != nil {
			return fmt.Errorf("rabbitmq cache.populated consumer: %w", err)
		}
		defer cons.Close()

		evtHandler := eventconsumer.NewHandler(s, store, repo)
		go func() {
			slog.Info("starting cache.populated consumer (FUT-017 auto-sign-on-cache)")
			if err := cons.Consume(ctx, evtHandler.Handle); err != nil {
				slog.Error("cache.populated consumer stopped", "error", err)
			}
		}()
	} else {
		slog.Info("FUT-017 cache.populated consumer not started",
			"have_rabbitmq", cfg.RabbitMQURL != "",
			"have_db", repo != nil)
	}

	// REDESIGN-001 Phase 3.4 — Single-tenant injector wiring. In
	// DEPLOYMENT_MODE=single signer dials services/tenant at startup,
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

	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	signerv1.RegisterSignerServiceServer(grpcSrv, grpcHdl)
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
	// to scrape :9090 without exposing the signer HTTP port to the cluster.
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
		slog.Info("shutting down signer service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// runMigrations opens a temporary *sql.DB, applies all pending goose migrations
// from the embedded FS, then closes it. The connection pool is opened separately
// so migrations complete before the server begins serving traffic.
func runMigrations(dsn string) error {
	// Reject sslmode=disable to prevent cleartext password transmission.
	if strings.Contains(strings.ToLower(dsn), "sslmode=disable") {
		return fmt.Errorf("SIGNER_DB_DSN must not use sslmode=disable")
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(signermigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("signer database migrations applied")
	return nil
}

// openPool creates and pings a pgxpool for the signer database.
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// buildGRPCOptions returns server options with interceptors and optional mTLS.
// extraUnary is the optional Phase 3.4 SingleTenantInjector (nil in multi mode).
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
// RPC + parse to libs/tenant/bootstrap.FetchTenantID (REDESIGN-001 Phase 3.4).
func fetchBootstrapTenantID(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.TenantGRPCAddr == "" {
		return "", fmt.Errorf("TENANT_GRPC_ADDR is required when DEPLOYMENT_MODE=single (Phase 3.4)")
	}
	tenantCreds, err := cfg.MTLSClientCreds("registry-tenant")
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

// loadSigner constructs a Signer from config. Each backend is independently
// instantiable so a deployment can switch backends by changing one env var.
func loadSigner(cfg *config.Config) (signing.Signer, error) {
	switch cfg.SignerKeyBackend {
	case "env":
		return signing.NewEnv(cfg.CosignPrivateKeyB64, cfg.CosignPublicKeyB64)
	case "vault":
		return signing.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultCosignPath)
	case "awskms", "gcpkms", "azurekms":
		return nil, fmt.Errorf("SIGNER_KEY_BACKEND=%s is not yet implemented; use env or vault backend", cfg.SignerKeyBackend)
	default:
		return nil, fmt.Errorf("unknown SIGNER_KEY_BACKEND: %s", cfg.SignerKeyBackend)
	}
}
