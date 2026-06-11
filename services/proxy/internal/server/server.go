// Package server wires all dependencies and runs the gRPC + HTTP servers.
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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
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

	// gRPC client connections — mTLS when certs are configured, insecure in dev.
	grpcCreds, err := clientCreds(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC credentials: %w", err)
	}

	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpcCreds)
	if err != nil {
		return fmt.Errorf("dial auth: %w", err)
	}
	defer authConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpcCreds)
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

	// gRPC server
	grpcSrv := grpc.NewServer()
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
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics.Handler().ServeHTTP(w, r)
	})

	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: http.MaxBytesHandler(mux, 4*1024*1024), // 4 MiB body limit (blobs are streamed)
	}

	errCh := make(chan error, 2)
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
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

// clientCreds returns mTLS transport credentials when cert paths are set,
// or insecure credentials for local development.
func clientCreds(cfg *config.Config) (grpc.DialOption, error) {
	if cfg.MTLSCACertPath != "" && cfg.MTLSCertPath != "" && cfg.MTLSKeyPath != "" {
		tlsCfg, err := mtls.ClientTLSConfig(cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, "")
		if err != nil {
			return nil, fmt.Errorf("load mTLS certs: %w", err)
		}
		return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
	}
	slog.Warn("mTLS not configured — gRPC clients running without TLS (development mode only)")
	return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
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
