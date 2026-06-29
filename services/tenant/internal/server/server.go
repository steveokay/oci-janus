// Package server wires the tenant service components and starts gRPC + HTTP servers.
package server

import (
	"context"
	"encoding/json"
	"errors"
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
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/tenant/internal/config"
	"github.com/steveokay/oci-janus/services/tenant/internal/handler"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
	tenantmigrations "github.com/steveokay/oci-janus/services/tenant/migrations"
)

// Run initialises all dependencies and starts the tenant service.
func Run(ctx context.Context, cfg *config.Config) error {
	// Use loader.DBConfig.PoolConfig() so that sslmode=disable is rejected at startup
	// (SEC-031) and pool tuning defaults are applied consistently with other services.
	// Environment plumbing engages PENTEST-017's dev-default credential rejection.
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

	// Pass the configured platform base domain so handler.GetTenant can build
	// the wildcard host `<slug>.<base>` (FE-API-007). Deployment mode is read
	// at config-load time and feeds the Phase 3.2 single-tenant guard in
	// CreateTenant.
	grpcHdl := handler.New(repo, cfg.PlatformBaseDomain, cfg.DeploymentMode)

	// REDESIGN-001 Phase 3.4 — in single-tenant mode every inbound RPC is
	// pinned to the bootstrap tenant. services/tenant is the canonical
	// source of GetDeploymentMetadata, so it can't self-dial like every
	// other service does — instead the bootstrap tenant id is read
	// directly from the repository (same DB it would serve over gRPC).
	//
	// Special case: the deployment may legitimately be pre-bootstrap on
	// first boot (the bootstrap CLI writes `bootstrap_tenant_id` *after*
	// the tenant service is up). In that window we skip wiring the
	// injector and log a warning so the operator sees what happened.
	// Once `make dev-bootstrap` (or the prod equivalent) runs, the next
	// restart picks it up. Phase 3.2's CreateTenant guard already
	// prevents a second tenant insertion in single mode, so the gap is
	// covered by a different invariant in the meantime.
	var singleTenantInterceptor grpc.UnaryServerInterceptor
	if cfg.DeploymentMode == loader.DeploymentModeSingle {
		bootstrapTenantID, err := readBootstrapTenantID(ctx, repo)
		switch {
		case errors.Is(err, repository.ErrNotFound):
			slog.Warn(
				"single-mode: bootstrap_tenant_id not yet written — SingleTenantInjector NOT wired this boot; run the bootstrap CLI then restart",
				"key", tenantbootstrap.DeploymentMetadataKey,
			)
		case err != nil:
			return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
		default:
			singleTenantInterceptor = grpcmw.SingleTenantInjector(bootstrapTenantID)
			slog.Info("single-mode tenant injector wired (self-read from local repo)",
				"bootstrap_tenant_id", bootstrapTenantID)
		}
	}

	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	tenantv1.RegisterTenantServiceServer(grpcSrv, grpcHdl)
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
	// to scrape :9090 without exposing the tenant service ports to the cluster.
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
		slog.Info("shutting down tenant service")
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

// readBootstrapTenantID is the services/tenant-specific equivalent of
// libs/tenant/bootstrap.FetchTenantID, but it reads directly from the
// local repository rather than dialling the gRPC RPC. services/tenant
// *is* the source of GetDeploymentMetadata so it cannot self-dial — and
// even if it could, doing so during Run() would race against its own
// listener coming up.
//
// Behaviour mirrors libs/tenant/bootstrap exactly so the same error
// shape ("parse <key> JSON", "<key> %q is not a valid UUID") shows up
// in logs no matter which service did the lookup:
//   - repo returns ErrNotFound     → propagate (caller treats as warn + skip)
//   - any other repo error         → wrap with the key name
//   - JSONB value isn't a JSON str → "parse <key> JSON: ..."
//   - JSON value isn't a UUID      → "<key> %q is not a valid UUID: ..."
func readBootstrapTenantID(ctx context.Context, repo *repository.Repository) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, tenantbootstrap.LookupTimeout)
	defer cancel()

	value, err := repo.GetDeploymentMetadata(callCtx, tenantbootstrap.DeploymentMetadataKey)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", err
		}
		return "", fmt.Errorf("GetDeploymentMetadata(%s): %w", tenantbootstrap.DeploymentMetadataKey, err)
	}

	var idStr string
	if err := json.Unmarshal(value, &idStr); err != nil {
		return "", fmt.Errorf("parse %s JSON: %w", tenantbootstrap.DeploymentMetadataKey, err)
	}
	if _, err := uuid.Parse(idStr); err != nil {
		return "", fmt.Errorf("%s %q is not a valid UUID: %w", tenantbootstrap.DeploymentMetadataKey, idStr, err)
	}
	return idStr, nil
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

	goose.SetBaseFS(tenantmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
