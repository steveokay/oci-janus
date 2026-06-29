// Package server wires all dependencies and runs the gRPC + HTTP servers.
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
	"github.com/redis/go-redis/v9"
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
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/config"
	"github.com/steveokay/oci-janus/services/proxy/internal/handler"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
	"github.com/steveokay/oci-janus/services/proxy/internal/upstream"
	proxymigrations "github.com/steveokay/oci-janus/services/proxy/migrations"
)

// Run wires everything and blocks until ctx is cancelled or a server errors.
func Run(ctx context.Context, cfg *config.Config) error {
	// Database pool
	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open db pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	// Run migrations
	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	// gRPC client connections — per-target mTLS creds with serverName pinned
	// to each remote's expected CN/SAN. SEC-039: a single shared `grpcCreds`
	// with empty serverName would skip the per-target CN/SAN check.
	authCreds, err := clientCreds(cfg, "registry-auth")
	if err != nil {
		return fmt.Errorf("build auth gRPC credentials: %w", err)
	}
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, authCreds)
	if err != nil {
		return fmt.Errorf("dial auth: %w", err)
	}
	defer authConn.Close()

	storageCreds, err := clientCreds(cfg, "registry-storage")
	if err != nil {
		return fmt.Errorf("build storage gRPC credentials: %w", err)
	}
	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, storageCreds)
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	// Repository
	repo := repository.New(pool)

	// Upstream HTTP client
	upstreamClient := upstream.New(cfg.UpstreamHTTPTimeoutSecs, cfg.UpstreamMaxResponseBytes)

	// gRPC handler
	grpcHandler, err := handler.NewGRPCHandler(repo, cfg.CredentialKeyHex)
	if err != nil {
		return fmt.Errorf("init grpc handler: %w", err)
	}

	// RabbitMQ publisher + store.queued consumer (optional — skipped when RABBITMQ_URL is unset).
	var pub *publisher.Publisher
	if cfg.RabbitMQURL != "" {
		p, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
		if err != nil {
			return fmt.Errorf("rabbitmq publisher: %w", err)
		}
		defer p.Close()
		pub = p
		slog.Info("rabbitmq publisher connected — blob store retries enabled")
	} else {
		slog.Warn("RABBITMQ_URL not set — failed background blob stores will not be retried")
	}

	// HTTP handler
	httpHandler, err := handler.NewHTTPHandler(repo, authConn, rdb, storageConn, upstreamClient, pub, cfg.CredentialKeyHex, cfg.AuthRealm)
	if err != nil {
		return fmt.Errorf("init http handler: %w", err)
	}

	// Start store.queued consumer when RabbitMQ is available.
	if cfg.RabbitMQURL != "" {
		storeCons, err := consumer.New(cfg.RabbitMQURL, consumer.Config{
			Queue:      "proxy.store.queued",
			RoutingKey: events.RoutingStoreQueued,
			MaxRetries: 3,
		})
		if err != nil {
			return fmt.Errorf("rabbitmq store consumer: %w", err)
		}
		defer storeCons.Close()
		// consume in background; errors are logged by the consumer loop.
		go func() {
			if err := storeCons.Consume(ctx, httpHandler.HandleStoreQueued); err != nil && ctx.Err() == nil {
				slog.Error("store consumer exited unexpectedly", "error", err)
			}
		}()
	}

	// REDESIGN-001 Phase 3.4 — in single-tenant mode every inbound RPC is
	// pinned to the bootstrap tenant via a server interceptor. The tenant ID
	// itself is fetched once at startup from registry-tenant's
	// GetDeploymentMetadata RPC. In multi mode the tenant ID comes from the
	// gateway/JWT and no interceptor is wired.
	var singleTenantInterceptor grpc.UnaryServerInterceptor
	if cfg.DeploymentMode == loader.DeploymentModeSingle {
		bootstrapTenantID, err := fetchBootstrapTenantID(ctx, cfg)
		if err != nil {
			return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
		}
		singleTenantInterceptor = grpcmw.SingleTenantInjector(bootstrapTenantID)
		slog.Info("single-mode tenant injector wired",
			"bootstrap_tenant_id", bootstrapTenantID,
			"tenant_grpc", cfg.TenantGRPCAddr)
	}

	// gRPC server — mTLS-aware when certs are configured, plaintext in dev.
	// Mirrors the auth / signer / metadata pattern. FUT-013 surfaced the
	// missing TLS wrap here: the management BFF dials with mTLS creds (and
	// has done since day one), so a plaintext server here would fail every
	// dial. The pre-FUT-013 stack happened to work only because nothing
	// dialled the proxy gRPC server externally.
	grpcOpts, err := buildGRPCServerOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build grpc opts: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthpb.RegisterHealthServer(grpcSrv, health.NewServer())
	proxyv1.RegisterProxyServiceServer(grpcSrv, grpcHandler)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	// HTTP server
	mux := http.NewServeMux()
	httpHandler.Register(mux)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// ReadHeaderTimeout prevents Slowloris attacks.
	// WriteTimeout is generous (60s) because this service streams upstream blobs
	// directly to Docker clients — transfers can take many seconds for large layers.
	// SecureHeaders is outermost so security headers appear on all responses.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 4*1024*1024)), // 4 MiB body limit (blobs are streamed)
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // 60s to allow large upstream layer streaming
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the proxy HTTP port to the cluster.
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

// buildGRPCServerOptions wires the standard interceptor chain plus mTLS
// server credentials when MTLS_* paths are configured. Mirrors the pattern
// in services/auth + services/signer.
//
// REDESIGN-001 Phase 3.4 added two things on top:
//   - the standard `grpcmw.ServerInterceptors()` chain (recovery / OTEL /
//     tracing / logging) which proxy was missing entirely — the gRPC
//     server previously ran with no interceptors at all, same gap closed
//     for scanner in PR #175.
//   - extraUnary, when non-nil, is chained after the standard chain so the
//     SingleTenantInjector runs after auth/tenant extraction but before
//     reaching handlers.
//
// Without the mTLS wrap here the proxy gRPC server runs plaintext while
// every other service in the stack runs mTLS — caused FUT-013's "tls:
// first record does not look like a TLS handshake" smoke-test failure.
func buildGRPCServerOptions(cfg *config.Config, extraUnary grpc.UnaryServerInterceptor) ([]grpc.ServerOption, error) {
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
			return nil, fmt.Errorf("load mTLS server certs: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		slog.Warn("mTLS not configured — gRPC server running without TLS (development mode only)")
	}
	return opts, nil
}

// fetchBootstrapTenantID looks up the bootstrap tenant UUID from
// registry-tenant's GetDeploymentMetadata RPC (REDESIGN-001 Phase 3.4).
//
// This is the single source of truth for "which tenant is this single-mode
// binary wired to" — the value is read once at boot, then handed to
// SingleTenantInjector for every inbound RPC. Errors fail the boot loudly
// because a misconfigured tenant address would otherwise route every proxy
// pull through the wrong tenant.
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
	defer func() { _ = tenantConn.Close() }()
	return tenantbootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
}

// clientCreds returns mTLS dial credentials with serverName pinned to the
// remote service's expected CN/SAN (e.g. "registry-auth", "registry-storage"),
// falling back to plaintext insecure for local dev without certs. SEC-039:
// the previous signature passed an empty serverName so no per-target
// CN/SAN pin was enforced. mtls.ClientCreds returns insecure.NewCredentials
// only when ALL cert paths are empty (dev posture); with paths set it
// returns the error on TLS load failure so a corrupted cert fails-loud
// instead of silently downgrading to plaintext.
func clientCreds(cfg *config.Config, serverName string) (grpc.DialOption, error) {
	creds, err := mtls.ClientCreds(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, serverName)
	if err != nil {
		return nil, fmt.Errorf("load mTLS certs: %w", err)
	}
	if cfg.MTLSCACertPath == "" || cfg.MTLSCertPath == "" || cfg.MTLSKeyPath == "" {
		slog.Warn("mTLS not configured — gRPC clients running without TLS (development mode only)")
	}
	return grpc.WithTransportCredentials(creds), nil
}

// runMigrations opens a temporary pgxpool and runs goose migrations from the embedded FS.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open migration pool: %w", err)
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	goose.SetBaseFS(proxymigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
