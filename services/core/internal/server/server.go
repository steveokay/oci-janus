// Package server wires all dependencies and runs the gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/core/internal/config"
	"github.com/steveokay/oci-janus/services/core/internal/handler"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

// Run wires everything and blocks until ctx is cancelled or a server errors.
func Run(ctx context.Context, cfg *config.Config) error {
	// Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	defer rdb.Close()

	// gRPC client transport: per-target mTLS creds with serverName pinned to
	// the remote's expected CN/SAN. SEC-039 — a single shared `grpcCreds`
	// with empty serverName would skip the per-target CN/SAN check.
	authCreds, err := cfg.MTLSClientCreds("registry-auth")
	if err != nil {
		return fmt.Errorf("load auth mTLS creds: %w", err)
	}
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(authCreds))
	if err != nil {
		return fmt.Errorf("dial auth: %w", err)
	}
	defer authConn.Close()

	metaCreds, err := cfg.MTLSClientCreds("registry-metadata")
	if err != nil {
		return fmt.Errorf("load metadata mTLS creds: %w", err)
	}
	metaConn, err := grpc.NewClient(cfg.MetadataGRPCAddr, grpc.WithTransportCredentials(metaCreds))
	if err != nil {
		return fmt.Errorf("dial metadata: %w", err)
	}
	defer metaConn.Close()

	storageCreds, err := cfg.MTLSClientCreds("registry-storage")
	if err != nil {
		return fmt.Errorf("load storage mTLS creds: %w", err)
	}
	storageConn, err := grpc.NewClient(cfg.StorageGRPCAddr, grpc.WithTransportCredentials(storageCreds))
	if err != nil {
		return fmt.Errorf("dial storage: %w", err)
	}
	defer storageConn.Close()

	// Trigger eager connection establishment so the first inbound request doesn't
	// stall while the gRPC TCP/HTTP-2 handshake completes.
	authConn.Connect()
	metaConn.Connect()
	storageConn.Connect()

	// RabbitMQ publisher
	pub, err := publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer pub.Close()

	// Service layer
	uploadStore := service.NewUploadStore(rdb)
	referrerStore := service.NewReferrerStore(rdb)
	authClient := service.NewAuthClient(authConn, rdb)
	// FE-API-042: PullEventSampleRate gates the pull.image publish on every
	// successful manifest GET. Validated at startup to be in [0.0, 1.0].
	registry := service.NewRegistry(metaConn, storageConn, uploadStore, referrerStore, pub, cfg.PullEventSampleRate)

	// Futures.md Tier 1 #3 — signed-image admission. Optional dial:
	// when SIGNER_GRPC_ADDR is unset we leave the signer field nil
	// and the admission gate logs+allows instead of failing closed
	// (dev-stack convenience). Production deployments always set this.
	if cfg.SignerGRPCAddr != "" {
		signerCreds, err := cfg.MTLSClientCreds("registry-signer")
		if err != nil {
			return fmt.Errorf("load signer mTLS creds: %w", err)
		}
		signerConn, err := grpc.NewClient(cfg.SignerGRPCAddr, grpc.WithTransportCredentials(signerCreds))
		if err != nil {
			return fmt.Errorf("dial signer: %w", err)
		}
		defer signerConn.Close()
		signerConn.Connect()
		registry = registry.WithSigner(signerConn)
		slog.Info("signed-image admission wired", "signer_grpc_addr", cfg.SignerGRPCAddr)
	} else {
		slog.Warn("SIGNER_GRPC_ADDR not set — repos with require_signature=true will warn+allow (dev only)")
	}

	// HTTP handler
	h := handler.New(authClient, registry, cfg.AuthRealm)
	mux := http.NewServeMux()
	h.Register(mux)

	// otelhttp wraps the handler tree to create a root span for every HTTP request.
	// SecureHeaders is inside so security headers appear on all responses including errors.
	// ReadTimeout and WriteTimeout are generous to accommodate blob streaming.
	// ReadHeaderTimeout is short to prevent Slowloris attacks.
	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: otelhttp.NewHandler(
			httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 1<<30)),
			"registry-core",
		),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // 60s to allow large blob layer transfers to complete
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the OCI business port to the cluster.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		metrics.Handler().ServeHTTP(w, r)
	})
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// gRPC server (health check only for now; future: expose internal gRPC if needed).
	// SEC-010: use standard interceptors (recovery, OTEL tracing, logging) so panics
	// are caught and observability is consistent with other services.
	//
	// REDESIGN-001 Phase 3.4 — Single-tenant injector wiring. In
	// DEPLOYMENT_MODE=single we look up the bootstrap tenant id from
	// services/tenant.deployment_metadata at startup so the server-side
	// interceptor can reject mismatched x-tenant-id metadata. Fail-loud:
	// if the lookup errors, core exits. In multi mode the dial is skipped.
	var singleTenantInterceptor grpc.UnaryServerInterceptor
	if cfg.DeploymentMode == loader.DeploymentModeSingle {
		bootstrapTenantID, err := fetchBootstrapTenantID(ctx, cfg)
		if err != nil {
			return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
		}
		singleTenantInterceptor = grpcmw.SingleTenantInjector(bootstrapTenantID)
		slog.Info("single-mode tenant injector wired",
			"bootstrap_tenant_id", bootstrapTenantID,
			"tenant_grpc", cfg.TenantGRPCAddr,
		)
	}

	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthpb.RegisterHealthServer(grpcSrv, health.NewServer())
	// Referrers tab (FE): expose the OCI referrers listing over gRPC so the
	// management BFF can call registry-core directly. Reuse the same Registry
	// already built for the HTTP OCI handler — never construct a second one.
	corev1.RegisterCoreServiceServer(grpcSrv, handler.NewCoreHandler(registry))

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	errCh := make(chan error, 3)
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
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

// buildGRPCOptions returns server options for the health-check gRPC server.
// Even though this server only exposes the health protocol, we attach the
// standard interceptor chain (panic recovery, OTEL tracing, structured logging)
// so that the health endpoint is observable and cannot panic-crash the process.
// mTLS is applied when cert paths are configured (SEC-010).
//
// extraUnary is the optional Phase 3.4 SingleTenantInjector (nil in multi mode).
func buildGRPCOptions(cfg *config.Config, extraUnary grpc.UnaryServerInterceptor) ([]grpc.ServerOption, error) {
	chain := grpcmw.ServerInterceptors()
	if extraUnary != nil {
		chain = append(chain, extraUnary)
	}
	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(chain...),
		grpc.ChainStreamInterceptor(grpcmw.StreamServerInterceptors()...),
		grpc.MaxSendMsgSize(20 << 20), // FUT-022: headroom above hardBlobCap (16 MiB) for gRPC framing
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

// fetchBootstrapTenantID dials services/tenant and delegates the post-dial
// RPC + parse to libs/tenant/bootstrap.FetchTenantID. Caller in Run() converts
// any error to a fatal startup failure (REDESIGN-001 Phase 3.4).
func fetchBootstrapTenantID(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.TenantGRPCAddr == "" {
		return "", fmt.Errorf("TENANT_GRPC_ADDR is required when DEPLOYMENT_MODE=single (Phase 3.4)")
	}
	tenantCreds, err := cfg.MTLSClientCreds("registry-tenant")
	if err != nil {
		return "", fmt.Errorf("build tenant gRPC creds: %w", err)
	}
	tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(tenantCreds))
	if err != nil {
		return "", fmt.Errorf("dial tenant gRPC: %w", err)
	}
	defer tenantConn.Close()
	return tenantbootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
}
