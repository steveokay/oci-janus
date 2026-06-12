// Package server wires the audit service: RabbitMQ consumer, HTTP API, gRPC health.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
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
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	auditmigrations "github.com/steveokay/oci-janus/services/audit/migrations"
	"github.com/steveokay/oci-janus/services/audit/internal/config"
	"github.com/steveokay/oci-janus/services/audit/internal/eventconsumer"
	"github.com/steveokay/oci-janus/services/audit/internal/handler"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// Run initialises all audit service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Run migrations first (on a plain pool) so the registry_audit_app role exists
	// before the main pool tries to SET ROLE to it.
	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Use loader.DBConfig.PoolConfig() so that sslmode=disable is rejected at startup
	// (SEC-031) and pool tuning defaults are applied consistently with other services.
	tmpDB := &loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns}
	poolCfg, err := tmpDB.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}
	// Every connection in the runtime pool assumes the low-privilege role so that
	// FORCE ROW LEVEL SECURITY on audit_events applies correctly (SEC-001).
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE registry_audit_app")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()

	if err := checkRole(ctx, pool); err != nil {
		return err
	}

	repo := repository.New(pool)

	// RabbitMQ consumer for all platform events.
	cons, err := consumer.New(cfg.RabbitMQURL, consumer.Config{
		Queue:      "audit.events",
		RoutingKey: "#",
		MaxRetries: 3,
		Exchange:   events.ExchangeEvents,
	})
	if err != nil {
		return fmt.Errorf("rabbitmq consumer: %w", err)
	}
	defer cons.Close()

	ec := eventconsumer.New(repo)
	go func() {
		slog.Info("audit: starting event consumer")
		if err := cons.Consume(ctx, ec.HandleEvent); err != nil {
			slog.Error("audit: consumer stopped", "error", err)
		}
	}()

	// Retention cleanup goroutine.
	go runRetentionLoop(ctx, repo, cfg.RetentionDays)

	// HTTP server: audit write endpoint + query endpoint + health + metrics.
	httpHdl := handler.New(repo)
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics.Handler().ServeHTTP(w, r)
	})
	httpMux.HandleFunc("POST /audit/events", httpHdl.WriteEvent)
	httpMux.HandleFunc("GET /audit/events", httpHdl.QueryEvents)

	// ReadHeaderTimeout prevents Slowloris attacks.
	// ReadTimeout and WriteTimeout bound the full request/response cycle.
	// SecureHeaders is outermost so security headers appear on all responses including
	// error responses from MaxBytesHandler before the inner mux runs.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(httpMux, 1*1024*1024)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// gRPC server: health check only (no audit-specific RPC yet).
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

	errCh := make(chan error, 2)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down audit service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// runRetentionLoop deletes audit events older than retentionDays once per day.
func runRetentionLoop(ctx context.Context, repo *repository.Repository, retentionDays int) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().AddDate(0, 0, -retentionDays)
			n, err := repo.PurgeOlderThan(ctx, cutoff)
			if err != nil {
				slog.Error("audit retention purge failed", "error", err)
			} else if n > 0 {
				slog.Info("audit retention: purged old events", "count", n, "cutoff", cutoff)
			}
		}
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

// checkRole verifies the pool is operating as registry_audit_app and not as the
// schema owner. If the AfterConnect SET ROLE failed silently, this catches it early.
func checkRole(ctx context.Context, pool *pgxpool.Pool) error {
	var currentUser string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		return fmt.Errorf("checkRole: %w", err)
	}
	if currentUser != "registry_audit_app" {
		return fmt.Errorf(
			"SEC-001: registry-audit must run as registry_audit_app role, got %q — "+
				"ensure GRANT registry_audit_app TO <login_user> was applied by the migration",
			currentUser,
		)
	}
	return nil
}

// runMigrations applies goose SQL migrations.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(auditmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
