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
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
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

	auditConn, err := grpc.NewClient(cfg.AuditGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
	if err != nil {
		return fmt.Errorf("dial audit grpc: %w", err)
	}
	defer auditConn.Close()

	// RabbitMQ publisher for scan.queued events (manually triggered scans).
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	authClient := authv1.NewAuthServiceClient(authConn)
	metaClient := metadatav1.NewMetadataServiceClient(metaConn)
	auditClient := auditv1.NewAuditServiceClient(auditConn)

	// tenant gRPC client is optional — only wired when TENANT_GRPC_ADDR is set
	// (enables the super-admin /api/v1/admin/tenants routes). nil disables them.
	var tenantClient tenantv1.TenantServiceClient
	if cfg.TenantGRPCAddr != "" {
		tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
		if err != nil {
			return fmt.Errorf("dial tenant grpc: %w", err)
		}
		defer tenantConn.Close()
		tenantClient = tenantv1.NewTenantServiceClient(tenantConn)
	}

	// webhook gRPC client is optional too — only wired when WEBHOOK_GRPC_ADDR is set
	// (enables the /api/v1/webhooks routes from FE-API-021..024).
	var webhookClient webhookv1.WebhookServiceClient
	if cfg.WebhookGRPCAddr != "" {
		webhookConn, err := grpc.NewClient(cfg.WebhookGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
		if err != nil {
			return fmt.Errorf("dial webhook grpc: %w", err)
		}
		defer webhookConn.Close()
		webhookClient = webhookv1.NewWebhookServiceClient(webhookConn)
	}

	// signer gRPC client is optional — wired only when SIGNER_GRPC_ADDR is set
	// (enables FE-API-003 /api/v1/.../signature). Nil leaves the route at
	// 404 "route disabled" so a management deployment without a signer
	// service still serves every other surface.
	var signerClient signerv1.SignerServiceClient
	if cfg.SignerGRPCAddr != "" {
		signerConn, err := grpc.NewClient(cfg.SignerGRPCAddr, grpc.WithTransportCredentials(grpcCreds))
		if err != nil {
			return fmt.Errorf("dial signer grpc: %w", err)
		}
		defer signerConn.Close()
		signerClient = signerv1.NewSignerServiceClient(signerConn)
	}

	h := handler.New(authClient, metaClient, auditClient, pub, cfg.PlatformAdminTenantID,
		healthpb.NewHealthClient(authConn),
		healthpb.NewHealthClient(metaConn),
		healthpb.NewHealthClient(auditConn),
	)
	h = h.WithTenantClient(tenantClient)
	h = h.WithWebhookClient(webhookClient)
	h = h.WithSignerClient(signerClient)
	// PENTEST-014: per-user read rate limit. 20 rps + burst 40 is sized for an
	// interactive dashboard while blocking a runaway script.
	h = h.WithRateLimiter(middleware.NewPerUserRateLimiter(20, 40))

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

