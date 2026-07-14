// Package server wires the audit service: RabbitMQ consumer, HTTP API, gRPC health.
package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/audit/internal/config"
	"github.com/steveokay/oci-janus/services/audit/internal/email"
	"github.com/steveokay/oci-janus/services/audit/internal/eventconsumer"
	"github.com/steveokay/oci-janus/services/audit/internal/export"
	"github.com/steveokay/oci-janus/services/audit/internal/exportworker"
	"github.com/steveokay/oci-janus/services/audit/internal/handler"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	"github.com/steveokay/oci-janus/services/audit/internal/scheduler"
	"github.com/steveokay/oci-janus/services/audit/internal/webhook"
	auditmigrations "github.com/steveokay/oci-janus/services/audit/migrations"
)

// Run initialises all audit service components and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	// Run migrations first (on a plain pool) so the registry_audit_app role exists
	// before the main pool tries to SET ROLE to it.
	if err := runMigrations(ctx, cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Use loader.DBConfig.PoolConfig() so that sslmode=disable is rejected at startup
	// (SEC-031) and pool tuning defaults are applied consistently with other services.
	// Environment plumbing engages PENTEST-017's dev-default credential rejection.
	tmpDB := &loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns, Environment: cfg.OTELEnvironment}
	poolCfg, err := tmpDB.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}
	// Every connection in the runtime pool assumes the low-privilege role so that
	// FORCE ROW LEVEL SECURITY on audit_events applies correctly (SEC-001).
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE registry_audit_app")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("pgxpool.New: %w", err)
	}
	defer pool.Close()

	if err := checkRole(ctx, pool); err != nil {
		return err
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
	// Audit-log streaming to SIEM (futures.md Tier 1 #4).
	//   Phase 1 fallback: in-process dispatcher (no DLX, lossy on
	//                     exhausted retries; kept for dev/legacy).
	//   Phase 2 (default): RabbitMQ audit.export queue + exportworker
	//                     consumer for durable delivery + drain.
	//
	// Both ride on the same secrets key — empty key disables both
	// paths so the audit service still boots cleanly without
	// streaming.
	var exportConsumer *exportworker.Consumer
	if k := exportSecretsKey(cfg); len(k) > 0 {
		// Always wire the legacy dispatcher as a safety net — if the
		// publisher's broker connection drops, audit events still
		// reach the SIEM via the in-process path (lossy, but better
		// than silent).
		ec = ec.WithExporter(eventconsumer.NewExportDispatcher(repo, k))
		// Phase 2 publisher + consumer. NewPublisher / NewConsumer
		// return nil + nil when the broker URL is empty so this
		// block is idempotent in dev.
		pub, err := exportworker.NewPublisher(cfg.RabbitMQURL)
		if err != nil {
			return fmt.Errorf("audit export publisher: %w", err)
		}
		if pub != nil {
			defer func() { _ = pub.Close() }()
			ec = ec.WithExportPublisher(pub)
		}
		cons, err := exportworker.NewConsumer(cfg.RabbitMQURL, repo, k)
		if err != nil {
			return fmt.Errorf("audit export consumer: %w", err)
		}
		if cons != nil {
			exportConsumer = cons
			go func() {
				slog.Info("audit-export worker starting")
				if err := cons.Run(ctx); err != nil && err != context.Canceled {
					slog.Error("audit-export worker stopped", "err", err)
				}
			}()
		}
	}
	_ = exportConsumer // referenced for future graceful-shutdown hook
	go func() {
		slog.Info("audit: starting event consumer")
		if err := cons.Consume(ctx, ec.HandleEvent); err != nil {
			slog.Error("audit: consumer stopped", "error", err)
		}
	}()

	// Retention cleanup goroutine.
	go runRetentionLoop(ctx, repo, cfg.RetentionDays)

	// FUT-019 Phase 3 — decode the email KEK once at boot. The per-tenant
	// email transport config (SMTP creds / Resend API key) is sealed with
	// this AES-256-GCM key; empty key idles the send loop. Decoded here (ahead
	// of the runner + sender goroutines) so both the send loop and the gRPC
	// handlers share the same key material. Reuses the 32-byte-checking helper.
	var emailKEK []byte
	if keyHex := cfg.NotifyEmailKeyHex; keyHex != "" {
		k, err := decodeHexKey(keyHex)
		if err != nil {
			return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: %w", err)
		}
		emailKEK = k
	}

	// FUT-019 webhook channel — decode the org-webhook HMAC KEK once at boot
	// (same 32-byte-checking helper as the email KEK). Empty idles the webhook
	// send loop and disables the runner's webhook fan-out.
	var webhookKEK []byte
	if keyHex := cfg.NotifyWebhookKeyHex; keyHex != "" {
		k, err := decodeHexKey(keyHex)
		if err != nil {
			return fmt.Errorf("NOTIFY_WEBHOOK_KEY_HEX: %w", err)
		}
		webhookKEK = k
	}

	// FUT-019 Phase 3 — dial registry-auth for recipient resolution. The
	// resolver adapter (authEmailResolver) is attached to the runner below and
	// the client is reused; dialing at boot keeps the mTLS creds + eager
	// connection setup alongside the other outbound dials (registry-tenant).
	// Optional: when AUTH_GRPC_ADDR is unset, email recipient resolution + the
	// email fan-out are disabled (runner keeps its nil resolver).
	var authClient authv1.AuthServiceClient
	if cfg.AuthGRPCAddr != "" {
		authCreds, err := cfg.MTLSClientCreds("registry-auth")
		if err != nil {
			return fmt.Errorf("build auth gRPC creds: %w", err)
		}
		authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(authCreds))
		if err != nil {
			return fmt.Errorf("dial auth gRPC: %w", err)
		}
		defer func() { _ = authConn.Close() }()
		// Eager connect so the first recipient-resolution RPC does not stall
		// on the TLS/HTTP-2 handshake (CLAUDE.md §6 gRPC conventions).
		authConn.Connect()
		authClient = authv1.NewAuthServiceClient(authConn)
	}

	// FUT-019 Phase 2 — scheduled-notifications scheduler + dispatcher.
	// Both loops live behind a single Runner; the scheduler ticks
	// hourly, the dispatcher every minute. Best-effort — failures log
	// and continue, the loops never panic the process.
	//
	// Phase 3 — attach the email recipient resolver only when registry-auth
	// is dialled; otherwise the runner keeps its nil resolver and skips the
	// email fan-out entirely.
	go func() {
		slog.Info("FUT-019: starting scheduled-notifications runner")
		runner := scheduler.New(repo, scheduler.Registry(), scheduler.RunnerConfig{})
		if authClient != nil {
			runner.WithEmailResolver(authEmailResolver{c: authClient})
		}
		// FUT-019 webhook channel — enable org-webhook fan-out only when the
		// webhook KEK is present (so the send loop can actually deliver).
		if len(webhookKEK) > 0 {
			runner.WithWebhookEnabled()
		}
		runner.Start(ctx)
		slog.Info("FUT-019: runner stopped")
	}()

	// FUT-019 Phase 3 — email send loop. Drains the email_deliveries queue and
	// sends via the per-tenant transport (Resend / SMTP). Idles when emailKEK
	// is empty (email channel disabled); runs alongside the runner goroutine.
	go func() {
		slog.Info("FUT-019: starting email sender loop")
		email.NewSender(repo, emailKEK, cfg.PlatformHost).Start(ctx)
		slog.Info("FUT-019: email sender stopped")
	}()

	// FUT-019 webhook channel — send loop. Drains notification_webhook_deliveries
	// and POSTs to the per-tenant org webhook. Idles when webhookKEK is empty.
	go func() {
		slog.Info("FUT-019: starting webhook sender loop")
		webhook.NewSender(repo, webhookKEK, cfg.PlatformHost).Start(ctx)
		slog.Info("FUT-019: webhook sender stopped")
	}()

	// HTTP server: liveness probe only.
	//
	// PENTEST-001 (2026-06-18): the unauthenticated POST/GET /audit/events
	// endpoints have been removed. Writes are now performed exclusively via the
	// RabbitMQ `eventconsumer` (durable + DLQ), and reads via the mTLS-gated
	// `AuditService` gRPC API consumed by `registry-management`. Re-introducing
	// an HTTP write/query API would require mTLS + CN allowlist on this port.
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// ReadHeaderTimeout prevents Slowloris attacks.
	// ReadTimeout and WriteTimeout bound the full request/response cycle.
	// SecureHeaders is outermost so security headers appear on all responses including
	// error responses from MaxBytesHandler before the inner mux runs.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(httpMux, 1*1024*1024)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the audit HTTP/gRPC ports to the cluster.
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

	// REDESIGN-001 Phase 3.4 / 9.3 — the platform is single-tenant (ADR-0031),
	// so every inbound RPC is pinned to the bootstrap tenant via a server
	// interceptor, wired unconditionally. The tenant ID itself is fetched once
	// at startup from registry-tenant's GetDeploymentMetadata RPC (single
	// source of truth for the workspace the binary is wired to).
	bootstrapTenantID, err := fetchBootstrapTenantID(ctx, cfg)
	if err != nil {
		return fmt.Errorf("phase 3.4 bootstrap tenant id lookup: %w", err)
	}
	singleTenantInterceptor := grpcmw.SingleTenantInjector(bootstrapTenantID)
	slog.Info("single-tenant injector wired",
		"bootstrap_tenant_id", bootstrapTenantID,
		"tenant_grpc", cfg.TenantGRPCAddr)

	// gRPC server: health check + AuditService (GetBuildHistory for management).
	grpcOpts, err := buildGRPCOptions(cfg, singleTenantInterceptor)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	// Register the AuditService so registry-management can query build history.
	// Audit-log streaming to SIEM (futures.md Tier 1 #4): decode the
	// hex secrets key here once at boot so the per-tenant CRUD
	// handlers can seal/unseal hmac_secret + bearer_token. Empty key
	// leaves the streaming RPCs functional for syslog-only flows
	// (no shared secret to encrypt) but rejects PUT requests that
	// carry a plaintext webhook secret with FailedPrecondition.
	var secretsKey []byte
	if keyHex := cfg.ExportSecretsKeyHex; keyHex != "" {
		k, err := decodeHexKey(keyHex)
		if err != nil {
			return fmt.Errorf("AUDIT_EXPORT_SECRETS_KEY_HEX: %w", err)
		}
		secretsKey = k
	}

	// emailKEK was decoded + authClient dialled earlier (ahead of the runner
	// + sender goroutines). Both are reused here to attach the email KEK +
	// transport factory to the gRPC handlers.

	auditHandler := handler.NewGRPC(repo).
		WithSecretsKey(secretsKey).
		WithExportTester(export.NewTester()).
		// FUT-019 Phase 3 — attach the email KEK + transport factory so the
		// SendTestEmail / per-tenant email CRUD handlers can unseal configs
		// and dial the SMTP/Resend transport.
		WithEmailKEK(emailKEK).
		WithEmailTransport(email.NewTransport).
		// FUT-019 webhook channel — attach the org-webhook HMAC KEK so the
		// Get/Put/SendTest handlers can seal/unseal the secret + post a test.
		WithWebhookKEK(webhookKEK)
	// REM-018-followup — wire the display-name resolver so GetNotifications
	// carries actor_display_name for the dashboard `<UserCell>`. Reuses the
	// registry-auth client dialled above (same client that resolves email
	// recipients). When AUTH_GRPC_ADDR is unset authClient is nil and the
	// notifications feed keeps its actor_username / actor_id fallback.
	if authClient != nil {
		auditHandler = auditHandler.WithActorResolver(authActorResolver{c: authClient})
	}
	// Phase 2 — wire the DLX probe + drain (futures.md Tier 1 #4).
	// Nil when RABBITMQ_URL is unset (legacy / unit-test stack) so
	// the handler falls back to Phase 1 behaviour (Drain returns
	// Unavailable, dlx_queue_depth = -1).
	if probe, err := exportworker.NewProbe(cfg.RabbitMQURL, cfg.RabbitMQMgmtURL); err != nil {
		return fmt.Errorf("audit export probe: %w", err)
	} else if probe != nil {
		auditHandler = auditHandler.WithExportDLXProbe(probe)
	}
	auditv1.RegisterAuditServiceServer(grpcSrv, auditHandler)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	errCh := make(chan error, 3)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(lis)
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("metrics server starting", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("metrics serve: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down audit service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// authEmailResolver adapts registry-auth's ResolveUserEmails RPC to the
// scheduler's EmailRecipientResolver interface (FUT-019 Phase 3). It turns a
// batch of user ids into a user-id→email map, silently dropping ids that fail
// to parse in the response.
type authEmailResolver struct{ c authv1.AuthServiceClient }

// ResolveEmails calls registry-auth.ResolveUserEmails for the given tenant +
// user ids and returns the resolved user-id→email map.
func (a authEmailResolver) ResolveEmails(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	strIDs := make([]string, len(ids))
	for i, id := range ids {
		strIDs[i] = id.String()
	}
	resp, err := a.c.ResolveUserEmails(ctx, &authv1.ResolveUserEmailsRequest{
		TenantId: tenantID.String(),
		UserIds:  strIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(resp.GetEmails()))
	for _, e := range resp.GetEmails() {
		if id, perr := uuid.Parse(e.GetUserId()); perr == nil {
			out[id] = e.GetEmail()
		}
	}
	return out, nil
}

// authActorResolver adapts registry-auth's LookupUsernames RPC to the audit
// handler's ActorDisplayNameResolver interface (REM-018-followup). It turns a
// batch of actor ids into an actor-id→display_name map so GetNotifications can
// populate NotificationEvent.actor_display_name. Only entries whose
// display_name is non-empty are returned — an empty display_name means the
// user never set one, and the FE renders the @username fallback anyway, so
// there's no value shipping it over the wire.
type authActorResolver struct{ c authv1.AuthServiceClient }

// ResolveDisplayNames calls registry-auth.LookupUsernames for the given tenant
// + actor ids and returns the resolved actor-id→display_name map. Errors bubble
// up to the handler, which fails open to the actor_username fallback.
func (a authActorResolver) ResolveDisplayNames(ctx context.Context, tenantID string, actorIDs []string) (map[string]string, error) {
	resp, err := a.c.LookupUsernames(ctx, &authv1.LookupUsernamesRequest{
		TenantId: tenantID,
		UserIds:  actorIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		if dn := u.GetDisplayName(); dn != "" {
			out[u.GetUserId()] = dn
		}
	}
	return out, nil
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

// buildGRPCOptions returns server options with interceptors and optional mTLS.
//
// extraUnary, when non-nil, is chained after libs/middleware/grpc.ServerInterceptors
// so the SingleTenantInjector (REDESIGN-001 Phase 3.4) runs after auth/tenant
// extraction but before reaching handlers.
func buildGRPCOptions(cfg *config.Config, extraUnary grpc.UnaryServerInterceptor) ([]grpc.ServerOption, error) {
	chain := grpcmw.ServerInterceptors()
	if extraUnary != nil {
		chain = append(chain, extraUnary)
	}
	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(chain...),
		grpc.ChainStreamInterceptor(grpcmw.StreamServerInterceptors()...),
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

// checkRole verifies the pool is operating as registry_audit_app and not as the
// schema owner. If the AfterConnect SET ROLE failed silently, this catches it early.
func checkRole(ctx context.Context, pool *pgxpool.Pool) error {
	var currentUser string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		return fmt.Errorf("checkRole: %w", err)
	}
	if currentUser != "registry_audit_app" {
		return fmt.Errorf(
			"SEC-001: registry-audit must run as registry_audit_app role, got %q — "+
				"ensure GRANT registry_audit_app TO <login_user> was applied by the migration",
			currentUser,
		)
	}
	return nil
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

	goose.SetBaseFS(auditmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// decodeHexKey parses the AES-256-GCM key configured via
// AUDIT_EXPORT_SECRETS_KEY_HEX. Must be exactly 32 bytes (64 hex
// chars) — same shape as the other AES-256-GCM keys in this codebase
// (proxy upstream credentials, SSO client_secret). Rejects any other
// length with a clear error so a typo doesn't silently produce a
// shorter key.
func decodeHexKey(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(b) != 32 {
		return nil, errors.New("expected 32 bytes (64 hex chars) of key material")
	}
	return b, nil
}

// fetchBootstrapTenantID looks up the bootstrap tenant UUID from
// registry-tenant's GetDeploymentMetadata RPC (REDESIGN-001 Phase 3.4).
//
// This is the single source of truth for "which tenant is this single-mode
// binary wired to" — the value is read once at boot, then handed to
// SingleTenantInjector for every inbound RPC. Errors fail the boot loudly
// because a misconfigured tenant address would otherwise route every audit
// write to the wrong tenant.
func fetchBootstrapTenantID(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg.TenantGRPCAddr == "" {
		return "", fmt.Errorf("TENANT_GRPC_ADDR is required (Phase 3.4)")
	}
	tenantCreds, err := cfg.MTLSClientCreds("registry-tenant")
	if err != nil {
		return "", fmt.Errorf("build tenant gRPC creds: %w", err)
	}
	tenantConn, err := grpc.NewClient(cfg.TenantGRPCAddr, grpc.WithTransportCredentials(tenantCreds))
	if err != nil {
		return "", fmt.Errorf("dial tenant gRPC: %w", err)
	}
	defer func() { _ = tenantConn.Close() }()
	return tenantbootstrap.FetchTenantID(ctx, tenantv1.NewTenantServiceClient(tenantConn))
}

// exportSecretsKey decodes AUDIT_EXPORT_SECRETS_KEY_HEX. Returns nil
// when unset so the caller can treat "no streaming key configured"
// uniformly. Errors panic — the boot path already validates the key
// shape via decodeHexKey before any goroutine runs; this helper is
// just to avoid re-decoding the key string in two places.
func exportSecretsKey(cfg *config.Config) []byte {
	if cfg.ExportSecretsKeyHex == "" {
		return nil
	}
	k, err := decodeHexKey(cfg.ExportSecretsKeyHex)
	if err != nil {
		return nil
	}
	return k
}
