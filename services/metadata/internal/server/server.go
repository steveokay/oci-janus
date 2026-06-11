// Package server wires the gRPC and HTTP servers together and manages graceful shutdown.
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
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/config"
	"github.com/steveokay/oci-janus/services/metadata/internal/handler"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
	metadatamigrations "github.com/steveokay/oci-janus/services/metadata/migrations"
)

// Run starts the gRPC and HTTP servers and blocks until ctx is cancelled.
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

	// ── 3. Read replica pool (REM-008) ────────────────────────────────────────
	// When DB_DSN_REPLICA is set, list-heavy reads (ListTags, ListRepositories,
	// ListOrphanedBlobs) are routed to the replica to offload the primary.
	var readPool *pgxpool.Pool
	if cfg.DBDSNReplica != "" {
		repCfg, err := cfg.DBConfig.ReplicaPoolConfig()
		if err != nil {
			return fmt.Errorf("build replica pool config: %w", err)
		}
		readPool, err = pgxpool.NewWithConfig(ctx, repCfg)
		if err != nil {
			return fmt.Errorf("connect to replica: %w", err)
		}
		defer readPool.Close()
		slog.Info("read replica connected", "addr", cfg.DBDSNReplica)
	} else {
		slog.Warn("DB_DSN_REPLICA not set — all reads go to primary (REM-008)")
	}

	// ── 4. Handler ────────────────────────────────────────────────────────────
	repo := repository.NewWithReplica(pool, readPool)
	h := handler.New(repo)

	// ── 5. gRPC server ────────────────────────────────────────────────────────
	grpcOpts, err := buildGRPCOptions(cfg, rdb)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)

	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	metadatav1.RegisterMetadataServiceServer(grpcSrv, h)
	healthSrv.SetServingStatus("registry.metadata.v1.MetadataService", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	// ── 6. HTTP server ────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", metricsHandler)
	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: http.MaxBytesHandler(mux, 4<<20),
	}

	// ── 7. Start & block ──────────────────────────────────────────────────────
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

func runMigrations(cfg *config.Config) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(metadatamigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("database migrations applied")
	return nil
}

func buildGRPCOptions(cfg *config.Config, rdb *redis.Client) ([]grpc.ServerOption, error) {
	// Cache interceptor for read-heavy metadata methods (REM-007).
	cacheInterceptor := grpcmw.CacheInterceptor(rdb, map[string]grpcmw.CachableMethod{
		"/registry.metadata.v1.MetadataService/GetRepository": {
			TTL: 30 * time.Second,
			KeyFunc: func(req proto.Message) string {
				r := req.(*metadatav1.GetRepositoryRequest)
				return r.GetTenantId() + ":" + r.GetRepoId()
			},
			New: func() proto.Message { return &metadatav1.Repository{} },
		},
		"/registry.metadata.v1.MetadataService/GetManifest": {
			TTL: 5 * time.Minute,
			KeyFunc: func(req proto.Message) string {
				r := req.(*metadatav1.GetManifestRequest)
				return r.GetTenantId() + ":" + r.GetRepoId() + ":" + r.GetReference()
			},
			New: func() proto.Message { return &metadatav1.Manifest{} },
		},
		"/registry.metadata.v1.MetadataService/GetTag": {
			TTL: 30 * time.Second,
			KeyFunc: func(req proto.Message) string {
				r := req.(*metadatav1.GetTagRequest)
				return r.GetTenantId() + ":" + r.GetRepoId() + ":" + r.GetName()
			},
			New: func() proto.Message { return &metadatav1.Tag{} },
		},
		"/registry.metadata.v1.MetadataService/GetTenantQuotaUsage": {
			TTL: 10 * time.Second,
			KeyFunc: func(req proto.Message) string {
				r := req.(*metadatav1.GetTenantQuotaUsageRequest)
				return r.GetTenantId()
			},
			New: func() proto.Message { return &metadatav1.QuotaUsage{} },
		},
	})

	interceptors := append(grpcmw.ServerInterceptors(), cacheInterceptor)

	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(interceptors...),
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
