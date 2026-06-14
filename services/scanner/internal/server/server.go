// Package server wires all scanner dependencies and starts gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/scanner/internal/config"
	"github.com/steveokay/oci-janus/services/scanner/internal/handler"
	internalPlugin "github.com/steveokay/oci-janus/services/scanner/internal/plugin"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
)

// Run starts all service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Validate and load the scanner plugin binary (checksum verified here — service
	// refuses to start if the binary has been tampered with).
	scannerPlugin, err := internalPlugin.New(cfg.PluginPath, cfg.PluginChecksum)
	if err != nil {
		return fmt.Errorf("load scanner plugin: %w", err)
	}

	// gRPC connections to upstream services.
	// TODO: replace insecure.NewCredentials() with mTLS creds from libs/auth/mtls once certs are wired.
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial metadata %s: %w", cfg.MetadataGRPCAddr, err)
	}
	defer metaConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial storage %s: %w", cfg.StorageGRPCAddr, err)
	}
	defer storageConn.Close()

	// RabbitMQ publisher for scan.completed / scan.policy_blocked events.
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	// Consumer for push.completed events (automatic scan on every image push).
	cons, err := consumer.New(cfg.RabbitMQURL, worker.ConsumerConfig())
	if err != nil {
		return fmt.Errorf("rabbitmq consumer: %w", err)
	}
	defer cons.Close()

	// Consumer for scan.queued events (manually triggered scans via management API).
	scanQueuedCons, err := consumer.New(cfg.RabbitMQURL, worker.ScanQueuedConsumerConfig())
	if err != nil {
		return fmt.Errorf("rabbitmq scan.queued consumer: %w", err)
	}
	defer scanQueuedCons.Close()

	scanStore := store.New()
	pool := worker.NewPool(
		scannerPlugin,
		metaConn,
		storageConn,
		pub,
		scanStore,
		cfg.WorkerCount,
		time.Duration(cfg.JobTimeoutSecs)*time.Second,
	)

	// Start worker pool goroutines — they block on the jobs channel until ctx is cancelled.
	go pool.Start(ctx, cfg.WorkerCount)

	// Consume push.completed events and dispatch to the worker pool.
	go func() {
		slog.Info("starting push.completed consumer")
		if err := cons.Consume(ctx, pool.HandlePushCompleted); err != nil {
			slog.Error("push.completed consumer stopped", "error", err)
		}
	}()

	// Consume scan.queued events for manually triggered scans (from management API).
	go func() {
		slog.Info("starting scan.queued consumer")
		if err := scanQueuedCons.Consume(ctx, pool.HandleScanQueued); err != nil {
			slog.Error("scan.queued consumer stopped", "error", err)
		}
	}()

	// gRPC server.
	grpcSrv := grpc.NewServer()
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	scannerv1.RegisterScannerServiceServer(grpcSrv, handler.New(pool, scanStore))
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics.Handler().ServeHTTP(w, r)
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
		slog.Info("shutting down scanner")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}
