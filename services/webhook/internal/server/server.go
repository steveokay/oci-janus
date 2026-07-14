// Package server wires the webhook service components and starts gRPC + HTTP servers.
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
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/webhook/internal/config"
	"github.com/steveokay/oci-janus/services/webhook/internal/delivery"
	"github.com/steveokay/oci-janus/services/webhook/internal/handler"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
	"github.com/steveokay/oci-janus/services/webhook/internal/worker"
	webhookmigrations "github.com/steveokay/oci-janus/services/webhook/migrations"

	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
)

// Run initialises all dependencies and starts the webhook service.
func Run(ctx context.Context, cfg *config.Config) error {
	// Use loader.DBConfig.PoolConfig() so that sslmode=disable is rejected at startup
	// (SEC-031) and pool tuning defaults are applied consistently with other services.
	// Pass Environment so PENTEST-017's dev-default credential check fires
	// in production/staging when the DSN embeds a known weak password.
	tmpDB := &loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns, Environment: cfg.OTELEnvironment}
	poolCfg, err := tmpDB.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	repo := repository.New(pool)
	dispatcher := delivery.NewDispatcher(cfg.DeliveryTimeoutSecs)

	// FUT-081: publisher for the webhook.queued/delivered/failed lifecycle
	// events. Same broker the consumer below uses; confirm mode is on by default.
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	w, err := worker.New(repo, dispatcher, pub, cfg.CredentialKeyHex, cfg.DeliveryPollIntervalSecs)
	if err != nil {
		return fmt.Errorf("new worker: %w", err)
	}

	// Single consumer with wildcard '#' routing key catches all event types.
	cons, err := consumer.New(cfg.RabbitMQURL, consumer.Config{
		Queue:      "webhook.events",
		RoutingKey: "#",
		MaxRetries: 3,
		Exchange:   events.ExchangeEvents,
	})
	if err != nil {
		return fmt.Errorf("rabbitmq consumer: %w", err)
	}
	defer cons.Close()

	go func() {
		slog.Info("starting event consumer")
		if err := cons.Consume(ctx, w.HandleEvent); err != nil {
			slog.Error("consumer stopped", "error", err)
		}
	}()

	go w.RunDeliveryLoop(ctx)

	// The handler reuses the worker's dispatcher for TestDispatch so the
	// SSRF guard + timeouts applied to production sends also apply to the
	// "send test" button in the dashboard.
	grpcHdl, err := handler.New(repo, dispatcher, cfg.CredentialKeyHex)
	if err != nil {
		return fmt.Errorf("new gRPC handler: %w", err)
	}

	// REDESIGN-001 Phase 3.4 / 9.3 — the platform is single-tenant (ADR-0031),
	// so webhook dials services/tenant at startup, fetches bootstrap_tenant_id,
	// and wires SingleTenantInjector into its gRPC unary chain unconditionally.
	// Fail-loud on lookup error.
	bootstrapTenantID, err := fetchBootstrapTenantID(ctx, cfg)
	if err != nil {
		return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
	}
	singleTenantInterceptor := grpcmw.SingleTenantInjector(bootstrapTenantID)
	slog.Info("single-tenant injector wired",
		"bootstrap_tenant_id", bootstrapTenantID,
		"tenant_grpc", cfg.TenantGRPCAddr,
	)

	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	webhookv1.RegisterWebhookServiceServer(grpcSrv, grpcHdl)
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
	// to scrape :9090 without exposing the webhook service ports to the cluster.
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
		slog.Info("shutting down webhook service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// buildGRPCOptions returns server options with interceptors and optional mTLS.
// extraUnary is the optional Phase 3.4 SingleTenantInjector (the nil check below is retained defensively).
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
		return "", fmt.Errorf("TENANT_GRPC_ADDR is required (Phase 3.4)")
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

// runMigrations runs goose SQL migrations against the database.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(webhookmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
