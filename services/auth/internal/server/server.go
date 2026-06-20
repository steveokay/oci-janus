// Package server wires the gRPC and HTTP servers together and manages graceful shutdown.
package server

import (
	"context"
	"encoding/hex"
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
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/auth/internal/config"
	"github.com/steveokay/oci-janus/services/auth/internal/handler"
	authmigrations "github.com/steveokay/oci-janus/services/auth/migrations"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	authsaml "github.com/steveokay/oci-janus/services/auth/internal/saml"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
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
	svc, err := service.New(users, apiKeys, rdb, cfg.JWTPrivateKeyB64, cfg.JWTPublicKeyB64, cfg.JWTKeyID)
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
	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)

	healthSrv := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	authv1.RegisterAuthServiceServer(grpcSrv, handler.NewGRPCHandler(svc, pub))
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
	httpH := handler.NewHTTPHandler(svc, devTenantID)

	// ── 5a. SSO sub-service (FE-API-034) ─────────────────────────────────
	// SSO is opt-in: without SSO_CREDENTIAL_KEY_HEX the routes are not
	// registered and the dashboard's SSO buttons keep their "coming soon"
	// behaviour. With it, the OAuth flow + admin CRUD endpoints come online.
	if cfg.SSOCredentialKeyHex != "" {
		key, err := hex.DecodeString(cfg.SSOCredentialKeyHex)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("SSO_CREDENTIAL_KEY_HEX must be a 64-character hex string (32 bytes)")
		}
		ssoSvc, err := service.NewSSO(
			svc,
			repository.NewAuthProviderRepository(pool),
			repository.NewLoginSessionRepository(pool),
			key,
		)
		if err != nil {
			return fmt.Errorf("init sso service: %w", err)
		}
		httpH = httpH.WithSSO(ssoSvc, cfg.SSOBaseURL)
		if pub != nil {
			httpH = httpH.WithEventPublisher(pub)
		}

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
			httpH = httpH.WithSAMLConfig(spCfg)
			slog.Info("SAML SP keypair loaded — /auth/saml/... routes active")
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

// buildGRPCOptions returns the server options list, including mTLS credentials if configured.
func buildGRPCOptions(cfg *config.Config) ([]grpc.ServerOption, error) {
	opts := []grpc.ServerOption{
		grpcmw.OTELServerHandler(),
		grpc.ChainUnaryInterceptor(grpcmw.ServerInterceptors()...),
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
