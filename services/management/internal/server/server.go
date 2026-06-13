// Package server wires gRPC clients, HTTP mux, and middleware for registry-management.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/config"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// Run starts the management HTTP server and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// In production mTLS creds are loaded from cert paths — see CLAUDE.md §7.
	// Insecure is acceptable in local dev where the Docker network is trusted.
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial auth grpc: %w", err)
	}
	defer authConn.Close()

	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial metadata grpc: %w", err)
	}
	defer metaConn.Close()

	authClient := authv1.NewAuthServiceClient(authConn)
	metaClient := metadatav1.NewMetadataServiceClient(metaConn)

	h := handler.New(authClient, metaClient)

	mux := http.NewServeMux()
	h.Register(mux)

	corsOrigin := cfg.CORSAllowedOrigin
	if corsOrigin == "" {
		slog.Warn("CORS_ALLOWED_ORIGIN not set — defaulting to http://localhost:5173 (dev only)")
		corsOrigin = "http://localhost:5173"
	}

	addr := cfg.HTTPAddr
	if addr == "" {
		addr = ":8085"
	}

	srv := &http.Server{
		Addr: addr,
		// Apply CORS then RequestID on every request, including preflight OPTIONS.
		Handler:      middleware.CORS(corsOrigin)(middleware.RequestID(mux)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("registry-management listening", "addr", addr)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}
