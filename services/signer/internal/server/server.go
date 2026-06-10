// Package server wires the signer service and starts gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/config"
	"github.com/steveokay/oci-janus/services/signer/internal/handler"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// Run initialises all dependencies and starts the signer service.
func Run(ctx context.Context, cfg *config.Config) error {
	s, err := loadSigner(cfg)
	if err != nil {
		return fmt.Errorf("load signer: %w", err)
	}
	slog.Info("signer loaded", "backend", cfg.SignerKeyBackend, "key_id", s.KeyID())

	store := sigstore.New()
	grpcHdl := handler.New(s, store)

	grpcSrv := grpc.NewServer()
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	signerv1.RegisterSignerServiceServer(grpcSrv, grpcHdl)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpMux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // TODO: wire prometheus registry
	})
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpMux,
		ReadHeaderTimeout: 10 * time.Second,
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
		slog.Info("shutting down signer service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// loadSigner constructs the Signer from config.
// Only the "env" backend is fully implemented; others fail with a clear error.
func loadSigner(cfg *config.Config) (*signing.Signer, error) {
	switch cfg.SignerKeyBackend {
	case "env":
		return signing.NewEnv(cfg.CosignPrivateKeyB64, cfg.CosignPublicKeyB64)
	case "vault", "awskms", "gcpkms", "azurekms":
		return nil, fmt.Errorf("SIGNER_KEY_BACKEND=%s is not yet implemented; use env backend", cfg.SignerKeyBackend)
	default:
		return nil, fmt.Errorf("unknown SIGNER_KEY_BACKEND: %s", cfg.SignerKeyBackend)
	}
}
