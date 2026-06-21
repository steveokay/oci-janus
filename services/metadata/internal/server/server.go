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
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/config"
	"github.com/steveokay/oci-janus/services/metadata/internal/handler"
	"github.com/steveokay/oci-janus/services/metadata/internal/pullconsumer"
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

	// ── 4b. FE-API-042 pull.image consumer ────────────────────────────────────
	// Drives manifests.last_pulled_at debounce-updates so the FE-API-043
	// max_idle_days retention rule has data to evaluate. The consumer runs in
	// its own goroutine with a dedicated queue ("metadata.pull.image") so it
	// never competes with the audit "#" subscription for delivery.
	if cfg.RabbitMQURL != "" {
		pullCons, err := consumer.New(cfg.RabbitMQURL, consumer.Config{
			Queue:      "metadata.pull.image",
			RoutingKey: events.RoutingPullImage,
			Exchange:   events.ExchangeEvents,
			MaxRetries: 3,
		})
		if err != nil {
			return fmt.Errorf("init pull.image consumer: %w", err)
		}
		defer pullCons.Close()
		pc := pullconsumer.New(repo)
		go func() {
			slog.Info("metadata: starting pull.image consumer", "queue", "metadata.pull.image")
			if err := pullCons.Consume(ctx, pc.HandleEvent); err != nil {
				slog.Error("metadata: pull.image consumer stopped", "error", err)
			}
		}()
	} else {
		slog.Warn("RABBITMQ_URL not set — pull.image consumer disabled, last_pulled_at will not be updated (FE-API-042)")
	}

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
	// ReadHeaderTimeout prevents Slowloris attacks.
	// ReadTimeout and WriteTimeout bound the full request/response cycle.
	// SecureHeaders is outermost so security headers appear on all responses.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 4<<20)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the metadata gRPC port to the cluster.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", metricsHandler)
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// ── 7. Start & block ──────────────────────────────────────────────────────
	errCh := make(chan error, 3)
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

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics.Handler().ServeHTTP(w, r)
}
