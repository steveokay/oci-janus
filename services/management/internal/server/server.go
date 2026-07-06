// Package server wires gRPC clients, HTTP mux, and middleware for registry-management.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/management/internal/config"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// Run starts the management HTTP server and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Per-target mTLS dial credentials — each gRPC client passes the remote's
	// expected CN/SAN to cfg.MTLSClientCreds so the TLS handshake fails closed
	// if the wrong service answers. SEC-039: the previous code shared a single
	// `grpcCreds` value built with empty serverName across all 9 dial sites,
	// skipping the per-target CN/SAN check. RED-FU-012: lifted the helper
	// itself onto loader.BaseConfig — see libs/config/loader/loader.go.
	authCreds, err := cfg.MTLSClientCreds("registry-auth")
	if err != nil {
		return fmt.Errorf("build auth grpc credentials: %w", err)
	}
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(authCreds))
	if err != nil {
		return fmt.Errorf("dial auth grpc: %w", err)
	}
	defer authConn.Close()

	metaCreds, err := cfg.MTLSClientCreds("registry-metadata")
	if err != nil {
		return fmt.Errorf("build metadata grpc credentials: %w", err)
	}
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(metaCreds))
	if err != nil {
		return fmt.Errorf("dial metadata grpc: %w", err)
	}
	defer metaConn.Close()

	auditCreds, err := cfg.MTLSClientCreds("registry-audit")
	if err != nil {
		return fmt.Errorf("build audit grpc credentials: %w", err)
	}
	auditConn, err := grpc.NewClient(cfg.AuditGRPCAddr, grpc.WithTransportCredentials(auditCreds))
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
		tenantCreds, err := cfg.MTLSClientCreds("registry-tenant")
		if err != nil {
			return fmt.Errorf("build tenant grpc credentials: %w", err)
		}
		tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(tenantCreds))
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
		webhookCreds, err := cfg.MTLSClientCreds("registry-webhook")
		if err != nil {
			return fmt.Errorf("build webhook grpc credentials: %w", err)
		}
		webhookConn, err := grpc.NewClient(cfg.WebhookGRPCAddr, grpc.WithTransportCredentials(webhookCreds))
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
		signerCreds, err := cfg.MTLSClientCreds("registry-signer")
		if err != nil {
			return fmt.Errorf("build signer grpc credentials: %w", err)
		}
		signerConn, err := grpc.NewClient(cfg.SignerGRPCAddr, grpc.WithTransportCredentials(signerCreds))
		if err != nil {
			return fmt.Errorf("dial signer grpc: %w", err)
		}
		defer signerConn.Close()
		signerClient = signerv1.NewSignerServiceClient(signerConn)
	}

	// scanner gRPC client is optional — wired only when SCANNER_GRPC_ADDR
	// is set (enables FE-API-018 scan policies and FE-API-019 compliance
	// reports). Nil leaves the routes returning 404 "route disabled".
	var scannerClient scannerv1.ScannerServiceClient
	if cfg.ScannerGRPCAddr != "" {
		scannerCreds, err := cfg.MTLSClientCreds("registry-scanner")
		if err != nil {
			return fmt.Errorf("build scanner grpc credentials: %w", err)
		}
		scannerConn, err := grpc.NewClient(cfg.ScannerGRPCAddr, grpc.WithTransportCredentials(scannerCreds))
		if err != nil {
			return fmt.Errorf("dial scanner grpc: %w", err)
		}
		defer scannerConn.Close()
		scannerClient = scannerv1.NewScannerServiceClient(scannerConn)
	}

	// FE-API-032 gc gRPC client. Optional — wired only when
	// GC_GRPC_ADDR is set. Nil leaves the `/api/v1/admin/gc/*` routes
	// returning 404 "route disabled" so deployments with gc still in
	// cron-only mode continue to serve every other surface.
	var gcClient gcv1.GCServiceClient
	if cfg.GCGRPCAddr != "" {
		gcCreds, err := cfg.MTLSClientCreds("registry-gc")
		if err != nil {
			return fmt.Errorf("build gc grpc credentials: %w", err)
		}
		gcConn, err := grpc.NewClient(cfg.GCGRPCAddr, grpc.WithTransportCredentials(gcCreds))
		if err != nil {
			return fmt.Errorf("dial gc grpc: %w", err)
		}
		defer gcConn.Close()
		gcClient = gcv1.NewGCServiceClient(gcConn)
	}

	// FUT-013 proxy gRPC client. Optional — wired only when
	// PROXY_GRPC_ADDR is set. Nil leaves the `/api/v1/proxy/cache*`
	// routes returning 404 "route disabled" so a management deployment
	// without registry-proxy still serves every other surface. The
	// frontend probes and hides the sidebar entry when 404 lands.
	var proxyClient proxyv1.ProxyServiceClient
	if cfg.ProxyGRPCAddr != "" {
		proxyCreds, err := cfg.MTLSClientCreds("registry-proxy")
		if err != nil {
			return fmt.Errorf("build proxy grpc credentials: %w", err)
		}
		proxyConn, err := grpc.NewClient(cfg.ProxyGRPCAddr, grpc.WithTransportCredentials(proxyCreds))
		if err != nil {
			return fmt.Errorf("dial proxy grpc: %w", err)
		}
		defer proxyConn.Close()
		proxyClient = proxyv1.NewProxyServiceClient(proxyConn)
	}

	// Referrers-tab core gRPC client. Optional — wired only when
	// CORE_GRPC_ADDR is set. Nil leaves the
	// `/api/v1/repositories/{org}/{repo}/tags/{tag}/referrers` route
	// returning 404 "route disabled" so a management deployment without
	// registry-core reachable over gRPC still serves every other surface.
	var coreClient corev1.CoreServiceClient
	if cfg.CoreGRPCAddr != "" {
		coreCreds, err := cfg.MTLSClientCreds("registry-core")
		if err != nil {
			return fmt.Errorf("build core grpc credentials: %w", err)
		}
		coreConn, err := grpc.NewClient(cfg.CoreGRPCAddr,
			grpc.WithTransportCredentials(coreCreds),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(20<<20)), // FUT-022: headroom above hardBlobCap (16 MiB) for gRPC framing
		)
		if err != nil {
			return fmt.Errorf("dial core grpc: %w", err)
		}
		defer coreConn.Close()
		coreClient = corev1.NewCoreServiceClient(coreConn)
	}

	h := handler.New(authClient, metaClient, auditClient, pub, cfg.PlatformAdminTenantID,
		healthpb.NewHealthClient(authConn),
		healthpb.NewHealthClient(metaConn),
		healthpb.NewHealthClient(auditConn),
	)
	h = h.WithTenantClient(tenantClient)
	h = h.WithWebhookClient(webhookClient)
	h = h.WithSignerClient(signerClient)
	h = h.WithScannerClient(scannerClient)
	h = h.WithGCClient(gcClient)
	h = h.WithProxyClient(proxyClient)
	h = h.WithCoreClient(coreClient)
	h = h.WithDeploymentInfo(cfg.DeploymentMode, cfg.BuildVersion)
	h = h.WithPlatformHost(cfg.PlatformHost)
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
