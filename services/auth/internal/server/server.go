// Package server wires the gRPC and HTTP servers together and manages graceful shutdown.
package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
	"github.com/steveokay/oci-janus/libs/config/loader"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	tenantbootstrap "github.com/steveokay/oci-janus/libs/tenant/bootstrap"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/config"
	"github.com/steveokay/oci-janus/services/auth/internal/handler"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authsaml "github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
)

// Run starts the gRPC and HTTP servers and blocks until ctx is cancelled or a server error occurs.
func Run(ctx context.Context, cfg *config.Config) error {
	// ── 1. Database ───────────────────────────────────────────────────────────
	if err := runMigrations(cfg); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	poolCfg, err := cfg.DBConfig.PoolConfig()
	if err != nil {
		return fmt.Errorf("build pool config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// ── 2. Redis ──────────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to Redis: %w", err)
	}

	// ── 3. Repositories & Service ─────────────────────────────────────────────
	users := repository.NewUserRepository(pool)
	apiKeys := repository.NewAPIKeyRepository(pool)
	sa := repository.NewServiceAccountRepo(pool)
	// audit is nil for now; the full AuditEmitter wiring (RabbitMQ publish or
	// direct audit-service call) ships in T13/T14. Cross-tenant attempts are
	// still rejected — the audit emission is best-effort.
	//
	// Phase 6.5 — branch on JWT_KEY_RING_PATH to pick between the legacy
	// single-key constructor and the multi-key ring constructor. Config-
	// layer validation already guarantees exactly one of the two paths is
	// active, so we do not need to defensively check both.
	svc, err := buildAuthService(users, apiKeys, sa, rdb, cfg)
	if err != nil {
		return fmt.Errorf("init service: %w", err)
	}

	// ── 3b. RabbitMQ publisher (RBAC audit events) ────────────────────────────
	// RABBITMQ_URL is optional for local dev without a broker; if absent, RBAC
	// events are skipped silently. In production the URL must be set.
	var pub *publisher.Publisher
	if cfg.RabbitMQURL != "" {
		pub, err = publisher.New(cfg.RabbitMQURL, events.ExchangeEvents)
		if err != nil {
			return fmt.Errorf("init rabbitmq publisher: %w", err)
		}
		defer pub.Close()
	} else {
		slog.Warn("RABBITMQ_URL not set — RBAC audit events will not be published")
	}

	// ── 4. gRPC server ────────────────────────────────────────────────────────
	//
	// REDESIGN-001 Phase 3.4 — Single-tenant injector wiring (pilot service).
	//
	// In DEPLOYMENT_MODE=single we look up the bootstrap tenant id from
	// services/tenant.deployment_metadata at startup so the server-side
	// interceptor can reject mismatched x-tenant-id metadata. Fail-loud:
	// if the lookup errors, auth exits — silently degrading this defence-in-
	// depth layer is worse than failing to start. In multi mode we skip the
	// dial entirely (the injector is a no-op for empty bootstrap id anyway).
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

	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	// FUT-001: build the audit emitter + OIDC trust service early so
	// both the gRPC handler and (later in §5a) the HTTP handler share
	// the same instance. The audit emitter is the same one used by
	// ServiceAccountService — it dispatches on AuditEvent.Action to
	// pick the right routing key + payload shape (see rabbitMQAuditEmitter).
	var saAudit service.AuditEmitter = slogAuditEmitter{}
	if pub != nil {
		saAudit = rabbitMQAuditEmitter{pub: pub}
	}
	oidcSvc := service.NewOIDCTrustService(
		repository.NewOIDCTrustRepo(pool),
		repository.NewServiceAccountRepo(pool),
		svc,
		saAudit,
		service.ParseIssuerAllowlist(cfg.OIDCAllowedIssuers),
	)
	authv1.RegisterAuthServiceServer(grpcSrv, handler.NewGRPCHandler(svc, pub).WithOIDCTrustService(oidcSvc))
	healthSrv.SetServingStatus("registry.auth.v1.AuthService", healthpb.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPCAddr, err)
	}

	// ── 5. HTTP server ────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	devTenantID, _ := uuid.Parse(cfg.DevDefaultTenantID)
	// QA-006: env-driven proxy CIDRs are parsed in main flow + handed to
	// the handler package, not in an init() that read os.Getenv directly.
	handler.SetTrustedProxies(handler.ParseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs))
	httpH := handler.NewHTTPHandler(svc, devTenantID)

	// ── 5a. Service-account service (FE-API-048 T13) ─────────────────────
	// Wire ServiceAccountService so /api/v1/service-accounts routes are live.
	// The audit emitter is a slog-only placeholder for now; durable audit
	// emission via RabbitMQ is a follow-up (FUT) item. The router accepts
	// *redis.Client directly — it satisfies the RedisCmdable interface.
	// FE-API-048 FUT-007: the saAudit emitter was built earlier (above
	// gRPC registration) so it could be shared with the FUT-001 OIDC
	// trust service. When a RabbitMQ publisher is wired, every SA
	// lifecycle event lands on events.RoutingServiceAccountLifecycle so
	// registry-audit can persist them as audit_events rows. Without a
	// broker (dev stacks) the slog stand-in keeps the trail visible in
	// container logs at INFO level — useful for local debugging but
	// invisible to the activity feed and audit dashboards.
	saSvc := service.NewServiceAccountService(
		repository.NewServiceAccountRepo(pool),
		users,
		apiKeys,
		saAudit,
		redisCmdableAdapter{rdb},
	)
	// Phase 6.7: wire the API-key validation cache invalidator so SA disable
	// / delete proactively wipes cached identities for the SA's keys. Without
	// this, a CI bot still holding a secret could keep authenticating off the
	// cache for up to apiKeyCacheTTL (the row-state check is the backstop,
	// but the proactive invalidation tightens the window from "up to TTL"
	// to "immediately").
	saSvc.SetAPIKeyCacheInvalidator(svc.InvalidateAPIKeyCache)
	httpH = httpH.WithServiceAccountService(saSvc)

	// ── 5a.1. Wire the FUT-001 OIDC trust service into the HTTP handler.
	// The service itself was constructed before the gRPC server (so the
	// gRPC handler could pick it up); we just attach it to the HTTP
	// handler here so POST /auth/token/workload is served.
	httpH = httpH.WithWorkloadExchange(oidcSvc, rdb)
	slog.Info("OIDC trust service wired",
		"allowed_issuers", cfg.OIDCAllowedIssuers,
	)

	// ── 5b. Activity service (FE-API-048 FUT-005) ────────────────────────
	// Dial registry-audit's gRPC server so /api/v1/access/activity can
	// resolve per-principal feeds. Optional — when AUDIT_GRPC_ADDR is
	// empty the route returns 501 NOT_IMPLEMENTED and the dashboard
	// activity tab falls back to the empty-state. Cert paths reuse the
	// gRPC-server mTLS material (same pattern as services/management).
	if cfg.AuditGRPCAddr != "" {
		auditCreds, err := cfg.MTLSClientCreds("registry-audit")
		if err != nil {
			return fmt.Errorf("build audit gRPC creds: %w", err)
		}
		auditConn, err := grpc.NewClient(cfg.AuditGRPCAddr, grpc.WithTransportCredentials(auditCreds))
		if err != nil {
			return fmt.Errorf("dial audit gRPC: %w", err)
		}
		defer auditConn.Close()
		auditClient := auditv1.NewAuditServiceClient(auditConn)
		activitySvc := service.NewActivityService(users, auditClient)
		httpH = httpH.WithActivityService(activitySvc)
		slog.Info("activity service wired", "audit_grpc", cfg.AuditGRPCAddr)
	} else {
		slog.Warn("AUDIT_GRPC_ADDR not set — /api/v1/access/activity returns 501")
	}

	// ── 5c. SSO sub-service (REDESIGN-001 RM-003) ────────────────────────
	// SSO is opt-in: without SSO_CREDENTIAL_KEY_HEX the routes are not
	// registered and the dashboard's SSO buttons keep their "coming soon"
	// behaviour. With it, the OAuth flow comes online. Per-tenant admin CRUD
	// routes have been removed (RM-003); providers are global and configured
	// via SQL / seed migrations.
	if cfg.SSOCredentialKeyHex != "" {
		key, err := hex.DecodeString(cfg.SSOCredentialKeyHex)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("SSO_CREDENTIAL_KEY_HEX must be a 64-character hex string (32 bytes)")
		}
		ssoSvc, err := service.NewSSO(
			svc,
			repository.NewGlobalSSOConfigRepository(pool),
			repository.NewLoginSessionRepository(pool),
			key,
		)
		if err != nil {
			return fmt.Errorf("init sso service: %w", err)
		}
		// RM-004: AUTH_DEFAULT_TENANT_ID is the fallback tenant for auto-
		// provisioned SSO users in single-tenant deployments. Optional; when
		// unset the callback returns an error for new users (they must be
		// pre-created by an admin). Multi-tenant routing is out of scope for
		// this PR.
		if cfg.DefaultTenantID != "" {
			dtid, err := uuid.Parse(cfg.DefaultTenantID)
			if err != nil {
				return fmt.Errorf("AUTH_DEFAULT_TENANT_ID is not a valid UUID: %w", err)
			}
			ssoSvc = ssoSvc.WithDefaultTenantID(dtid)
			slog.Info("SSO default tenant configured", "tenant_id", dtid)
		} else {
			slog.Warn("AUTH_DEFAULT_TENANT_ID not set — new SSO users will fail to auto-provision in single-tenant mode")
		}
		httpH = httpH.WithSSO(ssoSvc, cfg.SSOBaseURL)

		// SAML SP cert/key are independent of OAuth — operators can run with
		// only OAuth (cert paths empty) or both. Both paths must be set
		// together; one without the other is a config error.
		switch {
		case cfg.SAMLSPCertPath != "" && cfg.SAMLSPKeyPath != "":
			certPEM, err := os.ReadFile(cfg.SAMLSPCertPath)
			if err != nil {
				return fmt.Errorf("read SAML_SP_CERT_PATH: %w", err)
			}
			keyPEM, err := os.ReadFile(cfg.SAMLSPKeyPath)
			if err != nil {
				return fmt.Errorf("read SAML_SP_KEY_PATH: %w", err)
			}
			spCfg, err := authsaml.LoadSPConfig(certPEM, keyPEM)
			if err != nil {
				return fmt.Errorf("load SAML SP config: %w", err)
			}
			httpH = httpH.WithSAMLConfig(spCfg).WithSAMLTrustEmail(cfg.SSOSAMLTrustEmail)
			// REDESIGN-001 Phase 5.6 — surface the email-trust posture at
			// startup so operators can spot a misconfigured deployment from
			// the boot log alone.
			slog.Info("SAML SP keypair loaded — /auth/saml/... routes active",
				"sso_saml_trust_email", cfg.SSOSAMLTrustEmail)
			if !cfg.SSOSAMLTrustEmail {
				slog.Warn("SSO_SAML_TRUST_EMAIL is false (default, fail-safe) — SAML logins will be refused with 403 until the flag is set or the post-login email-verification flow lands")
			}
		case cfg.SAMLSPCertPath != "" || cfg.SAMLSPKeyPath != "":
			return fmt.Errorf("SAML_SP_CERT_PATH and SAML_SP_KEY_PATH must both be set or both empty")
		default:
			slog.Info("SAML SP keypair not configured — /auth/saml/... routes return 501")
		}

		// Background cleanup: drop expired login sessions every minute so
		// the auth_login_sessions table never grows beyond the active set.
		go runLoginSessionCleanup(ctx, ssoSvc)
		slog.Info("SSO routes registered", "base_url", cfg.SSOBaseURL)
	} else {
		slog.Warn("SSO_CREDENTIAL_KEY_HEX not set — SSO routes are disabled (dev only)")
	}

	httpH.Register(mux)

	// ReadHeaderTimeout prevents Slowloris attacks where a client holds connections
	// open by sending HTTP headers one byte at a time.
	// ReadTimeout caps the time for the full request body to arrive.
	// WriteTimeout caps the time to write the response (token responses are small).
	// SecureHeaders is the outermost wrapper so that security response headers
	// (X-Content-Type-Options, X-Frame-Options) appear on every response including
	// error responses generated by MaxBytesHandler before the inner handler runs.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(http.MaxBytesHandler(mux, 4<<20)), // 4 MiB request limit
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// ── 5b. Metrics server (SEC-025) ─────────────────────────────────────────
	// /metrics is served on a dedicated port so Kubernetes NetworkPolicy can
	// allow Prometheus to scrape :9090 without opening the business HTTP port.
	// No auth or SecureHeaders — this port is never exposed via the gateway.
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", metricsHandler)
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// ── 6. Start & block ──────────────────────────────────────────────────────
	errCh := make(chan error, 3)
	go func() {
		slog.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- fmt.Errorf("gRPC serve: %w", err)
		}
	}()
	go func() {
		slog.Info("HTTP server starting", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP serve: %w", err)
		}
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

// buildAuthService picks the right service constructor for the configured
// JWT-key posture (Phase 6.5). When JWTKeyRingPath is set, we load every PEM
// in the directory into a ring and hand it to NewWithKeyRing; otherwise we
// fall through to the legacy single-key New constructor.
//
// Fails loud on every error path — per CLAUDE.md §7, an unreadable ring
// directory must NOT silently fall back to single-key mode.
func buildAuthService(
	users *repository.UserRepository,
	apiKeys *repository.APIKeyRepository,
	saRepo *repository.ServiceAccountRepo,
	rdb *redis.Client,
	cfg *config.Config,
) (*service.Service, error) {
	if cfg.JWTKeyRingPath == "" {
		// Legacy single-key path — exactly what the pre-Phase-6.5 server
		// did. Internally still builds a 1-element ring so the runtime
		// behaviour is identical to the multi-key path.
		return service.New(users, apiKeys, saRepo, nil, rdb,
			cfg.JWTPrivateKeyB64, cfg.JWTPublicKeyB64, cfg.JWTKeyID)
	}
	// Multi-key path: load every PEM in the directory, build a ring, hand
	// it to the ring-aware constructor. The signing kid is either the
	// operator's nomination (JWT_SIGNING_KID) or — when empty — the
	// lexicographically-greatest kid (auto-promotion on restart for
	// monotonic naming conventions).
	ring, err := service.LoadKeyRing(cfg.JWTKeyRingPath, cfg.JWTSigningKID)
	if err != nil {
		return nil, fmt.Errorf("load JWT key ring: %w", err)
	}
	slog.Info("JWT key ring loaded",
		"path", cfg.JWTKeyRingPath,
		"signing_kid", ring.SigningKID(),
		"kid_count", ring.Size(),
	)
	return service.NewWithKeyRing(users, apiKeys, saRepo, nil, rdb, ring)
}

// runMigrations opens a temporary *sql.DB, applies goose migrations, then closes it.
// The connection pool is created separately so migrations run before traffic is accepted.
func runMigrations(cfg *config.Config) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(authmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("database migrations applied")
	return nil
}

// fetchBootstrapTenantID dials the tenant gRPC server, calls
// GetDeploymentMetadata(key="bootstrap_tenant_id"), and returns the UUID
// string ready to hand to SingleTenantInjector.
//
// REDESIGN-001 Phase 3.4 (services/auth pilot). Every backend service will
// follow this same pattern once the rollout fans out — the helper lives in
// each service rather than libs/ so the tenant client dial keeps the existing
// "per-service config var" idiom (TENANT_GRPC_ADDR alongside AUDIT_GRPC_ADDR).
//
// Behaviour:
//   - TENANT_GRPC_ADDR empty in single mode → error. Operators must wire it.
//   - tenant RPC returns NotFound → error ("not bootstrapped yet"). Operators
//     run the bootstrap CLI before starting auth.
//   - any other RPC error → propagated. Caller in Run() converts to a fatal
//     startup failure.
//   - JSONB value parse failure → error. The deployment_metadata schema
//     guarantees this is JSON-encoded; a parse failure means corruption.
//
// The dial uses the same mTLS material as the audit client (via
// cfg.MTLSClientCreds, lifted to loader.BaseConfig in RED-FU-012); in dev
// (cert paths unset) it falls back to plaintext with a slog.Warn from the
// credentials helper itself.
//
// The call is bounded by a 5-second context timeout — the tenant service must
// be reachable for auth to start in single mode anyway, so blocking startup
// longer than that just hides the misconfiguration.
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

// buildGRPCOptions returns the server options list, including mTLS credentials
// if configured.
//
// extraUnary is an optional additional unary interceptor appended to the end of
// the shared chain. REDESIGN-001 Phase 3.4 uses it to slot the
// SingleTenantInjector into the chain when DEPLOYMENT_MODE=single, so the
// "normalise the inbound x-tenant-id" defence runs after auth + tracing.
// Pass nil in multi mode (or when no extra interceptor is required).
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

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics.Handler().ServeHTTP(w, r)
}

// runLoginSessionCleanup periodically drops expired SSO login sessions so
// the auth_login_sessions table never grows beyond the active redirect
// dances. 60 seconds is a sweet spot — short enough that an attacker who
// captures a state token has at most ~1 minute of extra lifetime past the
// 10-minute hard TTL, long enough that the load on the DB is trivial.
func runLoginSessionCleanup(ctx context.Context, sso *service.SSO) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			deleted, err := sso.Sessions().DeleteExpired(cctx)
			cancel()
			if err != nil {
				slog.Warn("sso cleanup: DeleteExpired", "err", err)
				continue
			}
			if deleted > 0 {
				slog.Debug("sso cleanup: dropped expired sessions", "n", deleted)
			}
		}
	}
}

// slogAuditEmitter logs ServiceAccount lifecycle audit events to slog at INFO
// level. Used in local-dev stacks that boot without RabbitMQ — operators get
// a structured log line per mutation but the events don't reach
// services/audit, so they don't appear in /api/v1/access/activity or the
// audit dashboards. Production stacks should always have a broker wired so
// rabbitMQAuditEmitter (below) is used instead.
type slogAuditEmitter struct{}

// Emit logs the event and returns nil. Per spec §5.7 "audit failure is a hard
// error" — but for the slog stand-in we are best-effort: slog never errors, so
// the call site treats every emit as a success.
func (slogAuditEmitter) Emit(ctx context.Context, ev service.AuditEvent) error {
	slog.InfoContext(ctx, "audit",
		"tenant_id", ev.TenantID,
		"action", ev.Action,
		"actor_id", ev.ActorID,
		"resource", ev.Resource,
		"fields", ev.Fields,
	)
	return nil
}

// rabbitMQAuditEmitter publishes audit events to the registry.events
// topic exchange. It dispatches on the AuditEvent.Action to pick the
// right routing key + payload shape:
//
//   - FUT-001 OIDC trust + workload exchange actions land on the
//     matching auth.* routing keys with their own payload types.
//   - Every other action (the existing FE-API-048 SA lifecycle
//     vocabulary) lands on RoutingServiceAccountLifecycle as before.
//
// Publish errors are returned to the caller — spec §5.7 treats audit
// emit failures as hard errors; a broker outage fails the mutation
// rather than silently losing the audit row.
type rabbitMQAuditEmitter struct {
	pub *publisher.Publisher
}

// Emit dispatches on ev.Action to pick the routing key + payload shape.
// TenantID is carried on the outer envelope so a future per-tenant
// audit consumer can filter without unmarshalling the payload.
func (e rabbitMQAuditEmitter) Emit(ctx context.Context, ev service.AuditEvent) error {
	switch ev.Action {
	case events.RoutingOIDCTrustCreated, events.RoutingOIDCTrustUpdated, events.RoutingOIDCTrustDeleted:
		return e.publishOIDCTrust(ctx, ev)
	case events.RoutingWorkloadTokenExchanged, events.RoutingWorkloadTokenRejected:
		return e.publishWorkloadToken(ctx, ev)
	default:
		return e.publishSALifecycle(ctx, ev)
	}
}

// publishSALifecycle is the original (pre-FUT-001) lifecycle path —
// marshals the AuditEvent into ServiceAccountLifecyclePayload and
// publishes to RoutingServiceAccountLifecycle. registry-audit's
// eventconsumer translates each message to an audit_events row with
// action == payload.Action so spec §5.7's lifecycle vocabulary becomes a
// real, queryable audit trail.
func (e rabbitMQAuditEmitter) publishSALifecycle(ctx context.Context, ev service.AuditEvent) error {
	payload, err := json.Marshal(events.ServiceAccountLifecyclePayload{
		Action:   ev.Action,
		ActorID:  ev.ActorID,
		Resource: ev.Resource,
		Fields:   ev.Fields,
	})
	if err != nil {
		return fmt.Errorf("marshal SA lifecycle payload: %w", err)
	}
	envelope := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingServiceAccountLifecycle,
		TenantID:   ev.TenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := e.pub.Publish(ctx, events.RoutingServiceAccountLifecycle, envelope); err != nil {
		return fmt.Errorf("publish SA lifecycle: %w", err)
	}
	return nil
}

// publishOIDCTrust marshals the trust-mutation Fields into an
// OIDCTrustPayload and publishes to the matching auth.oidc_trust.*
// routing key (the routing key IS the Action).
func (e rabbitMQAuditEmitter) publishOIDCTrust(ctx context.Context, ev service.AuditEvent) error {
	payload, err := json.Marshal(events.OIDCTrustPayload{
		TrustID:          stringField(ev.Fields, "trust_id"),
		TenantID:         ev.TenantID,
		ServiceAccountID: stringField(ev.Fields, "service_account_id"),
		DisplayName:      stringField(ev.Fields, "display_name"),
		IssuerURL:        stringField(ev.Fields, "issuer_url"),
		Audience:         stringField(ev.Fields, "audience"),
		SubjectPattern:   stringField(ev.Fields, "subject_pattern"),
		ActorID:          ev.ActorID,
	})
	if err != nil {
		return fmt.Errorf("marshal oidc trust payload: %w", err)
	}
	envelope := events.Event{
		ID:         uuid.New().String(),
		Type:       ev.Action,
		TenantID:   ev.TenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := e.pub.Publish(ctx, ev.Action, envelope); err != nil {
		return fmt.Errorf("publish oidc trust: %w", err)
	}
	return nil
}

// publishWorkloadToken marshals the exchange/rejected Fields into a
// WorkloadTokenPayload and publishes to the matching
// auth.workload_token.* routing key.
func (e rabbitMQAuditEmitter) publishWorkloadToken(ctx context.Context, ev service.AuditEvent) error {
	payload, err := json.Marshal(events.WorkloadTokenPayload{
		TrustID:          stringField(ev.Fields, "trust_id"),
		IssuerURL:        stringField(ev.Fields, "issuer_url"),
		Subject:          stringField(ev.Fields, "subject"),
		ServiceAccountID: stringField(ev.Fields, "service_account_id"),
		Reason:           stringField(ev.Fields, "reason"),
	})
	if err != nil {
		return fmt.Errorf("marshal workload token payload: %w", err)
	}
	envelope := events.Event{
		ID:         uuid.New().String(),
		Type:       ev.Action,
		TenantID:   ev.TenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := e.pub.Publish(ctx, ev.Action, envelope); err != nil {
		return fmt.Errorf("publish workload token: %w", err)
	}
	return nil
}

// stringField is a small helper that extracts a string from the
// AuditEvent.Fields map, returning "" when missing or wrong-typed.
// Used by the FUT-001 publishers so a producer that forgets a field
// doesn't crash the publish.
func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// redisCmdableAdapter wraps *redis.Client so its Set/Del methods satisfy
// service.RedisCmdable's structural interface — the interface requires the
// return type to be exactly interface{Err() error}, but *redis.Client's
// methods return *redis.StatusCmd / *redis.IntCmd (which themselves implement
// that interface). Go's interface satisfaction is structural per method
// signature, so we adapt.
type redisCmdableAdapter struct{ c *redis.Client }

func (a redisCmdableAdapter) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) interface{ Err() error } {
	return a.c.Set(ctx, key, value, expiration)
}

func (a redisCmdableAdapter) Del(ctx context.Context, keys ...string) interface{ Err() error } {
	return a.c.Del(ctx, keys...)
}
