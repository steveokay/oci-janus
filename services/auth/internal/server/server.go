// Package server wires the gRPC and HTTP servers together and manages graceful shutdown.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	"github.com/steveokay/oci-janus/services/auth/internal/config"
	"github.com/steveokay/oci-janus/services/auth/internal/handler"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// Run starts the gRPC and HTTP servers and blocks until ctx is cancelled or a server error occurs.
func Run(ctx context.Context, cfg *config.Config) error {
	// ── 1. Database ───────────────────────────────────────────────────────────
	if err := runMigrations(cfg); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	poolCfg, err := cfg.DBConfig.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// ── 2. Redis ──────────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}

	// ── 3. Repositories & Service ─────────────────────────────────────────────
	users := repository.NewUserRepository(pool)
	apiKeys := repository.NewAPIKeyRepository(pool)
	svc, err := service.New(users, apiKeys, rdb, cfg.JWTPrivateKeyB64, cfg.JWTPublicKeyB64, cfg.JWTKeyID)
	if err != nil {
		return fmt.Errorf("init service: %w", err)
	}

	// ── 4. gRPC server ────────────────────────────────────────────────────────
	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)

	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	authv1.RegisterAuthServiceServer(grpcSrv, handler.NewGRPCHandler(svc))
	healthSrv.SetServingStatus("registry.auth.v1.AuthService", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	// ── 5. HTTP server ────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", metricsHandler)
	handler.NewHTTPHandler(svc).Register(mux)

	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: http.MaxBytesHandler(mux, 4<<20), // 4 MiB request limit
	}

	// ── 6. Start & block ──────────────────────────────────────────────────────
	errCh := make(chan error, 2)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- fmt.Errorf("gRPC serve: %w", err)
		}
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// runMigrations opens a temporary *sql.DB, applies goose migrations, then closes it.
// The connection pool is created separately so migrations run before traffic is accepted.
func runMigrations(cfg *config.Config) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("database migrations applied")
	return nil
}

// buildGRPCOptions returns the server options list, including mTLS credentials if configured.
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

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	// TODO: wire up prometheus registry
	w.WriteHeader(http.StatusOK)
}
