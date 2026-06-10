// Package server wires the audit service: RabbitMQ consumer, HTTP API, gRPC health.
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
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/audit/internal/config"
	"github.com/steveokay/oci-janus/services/audit/internal/eventconsumer"
	"github.com/steveokay/oci-janus/services/audit/internal/handler"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// Run initialises all audit service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("parse DB_DSN: %w", err)
	}
	poolCfg.MaxConns = cfg.DBMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()

	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
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
	httpMux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // TODO: wire prometheus
	})
	httpMux.HandleFunc("POST /audit/events", httpHdl.WriteEvent)
	httpMux.HandleFunc("GET /audit/events", httpHdl.QueryEvents)

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           http.MaxBytesHandler(httpMux, 1*1024*1024),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// gRPC server: health check only (no audit-specific RPC yet).
	grpcSrv := grpc.NewServer()
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

// runMigrations applies goose SQL migrations.
func runMigrations(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, "migrations")
}
