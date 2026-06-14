// Package server wires gRPC clients, HTTP mux, and middleware for registry-management.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/config"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// Run starts the management HTTP server and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Build gRPC transport credentials. When all three mTLS cert paths are
	// populated we use mutual TLS (required in production). In dev/test
	// environments where the paths are empty we fall back to plaintext —
	// config.validate() rejects this combination in production (OTEL_ENVIRONMENT=production).
	grpcCreds, err := buildGRPCCreds(cfg)
	if err != nil {
		return fmt.Errorf("build grpc credentials: %w", err)
	}

	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
	if err != nil {
		return fmt.Errorf("dial auth grpc: %w", err)
	}
	defer authConn.Close()

	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
	if err != nil {
		return fmt.Errorf("dial metadata grpc: %w", err)
	}
	defer metaConn.Close()

	// Use the RBAC-extended client so the handler can call GrantRole, RevokeRole,
	// and ListMembers in addition to the base ValidateToken / GetUserPermissions RPCs.
	authClient := authv1.NewAuthServiceClientWithRBAC(authConn)
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

	slog.Info("registry-management listening", "addr", addr, "mtls", cfg.MTLSCACertPath != "")

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

// buildGRPCCreds returns mTLS credentials when all three cert paths are
// configured, or plaintext insecure credentials otherwise (dev only).
// config.validate() ensures this plaintext path is rejected in production.
func buildGRPCCreds(cfg *config.Config) (credentials.TransportCredentials, error) {
	if cfg.MTLSCACertPath == "" || cfg.MTLSCertPath == "" || cfg.MTLSKeyPath == "" {
		return insecure.NewCredentials(), nil
	}
	tlsCfg, err := mtls.ClientTLSConfig(
		cfg.MTLSCACertPath,
		cfg.MTLSCertPath,
		cfg.MTLSKeyPath,
		// serverName left empty — the server name comes from the addr in production
		// (registry-auth.<namespace>.svc.cluster.local and similar). If a fixed server
		// name override is needed, add it as an env var in a follow-up.
		"",
	)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}

