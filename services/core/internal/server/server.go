// Package server wires all dependencies and runs the gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/services/core/internal/config"
	"github.com/steveokay/oci-janus/services/core/internal/handler"
	"github.com/steveokay/oci-janus/services/core/internal/service"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// Run wires everything and blocks until ctx is cancelled or a server errors.
func Run(ctx context.Context, cfg *config.Config) error {
	// Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	defer rdb.Close()

	// gRPC client connections (insecure for now; production uses mTLS from libs/auth/mtls)
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial auth: %w", err)
	}
	defer authConn.Close()

	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial metadata: %w", err)
	}
	defer metaConn.Close()

	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	// RabbitMQ publisher
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	// Service layer
	uploadStore := service.NewUploadStore(rdb)
	authClient := service.NewAuthClient(authConn, rdb)
	registry := service.NewRegistry(metaConn, storageConn, uploadStore, pub)

	// HTTP handler
	h := handler.New(authClient, registry, cfg.AuthRealm)
	mux := http.NewServeMux()
	h.Register(mux)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: http.MaxBytesHandler(mux, 1<<30), // 1 GiB total (individual endpoints impose stricter limits)
	}

	// gRPC server (health check only for now; future: expose internal gRPC if needed)
	grpcSrv := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, health.NewServer())

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
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
