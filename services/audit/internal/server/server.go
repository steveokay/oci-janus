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
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/audit/internal/config"
	"github.com/steveokay/oci-janus/services/audit/internal/eventconsumer"
	"github.com/steveokay/oci-janus/services/audit/internal/export"
	"github.com/steveokay/oci-janus/services/audit/internal/exportworker"
	"github.com/steveokay/oci-janus/services/audit/internal/handler"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	"github.com/steveokay/oci-janus/services/audit/internal/scheduler"
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

	// FUT-019 Phase 2 — scheduled-notifications scheduler + dispatcher.
	// Both loops live behind a single Runner; the scheduler ticks
	// hourly, the dispatcher every minute. Best-effort — failures log
	// and continue, the loops never panic the process.
	go func() {
		slog.Info("FUT-019: starting scheduled-notifications runner")
		runner := scheduler.New(repo, scheduler.Registry(), scheduler.RunnerConfig{})
		runner.Start(ctx)
		slog.Info("FUT-019: runner stopped")
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

	// gRPC server: health check + AuditService (GetBuildHistory for management).
	grpcOpts, err := buildGRPCOptions(cfg)
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
	auditHandler := handler.NewGRPC(repo).
		WithSecretsKey(secretsKey).
		WithExportTester(export.NewTester())
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
