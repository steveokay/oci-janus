// Package server wires the signer service and starts gRPC + HTTP servers.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/libs/auth/mtls"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/libs/observability/metrics"
	"github.com/steveokay/oci-janus/services/signer/internal/config"
	"github.com/steveokay/oci-janus/services/signer/internal/handler"
	signermigrations "github.com/steveokay/oci-janus/services/signer/migrations"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
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

	// ── Database (optional in dev, required in production) ──────────────────
	// When SIGNER_DB_DSN is set, run goose migrations and open the pool.
	// When absent, log a startup warning and fall back to an in-memory store.
	// The in-memory store loses all records on process restart — this is
	// acceptable for local development but NOT for production (SEC-015).
	var store *sigstore.Store
	if cfg.DBDSN != "" {
		if err := runMigrations(cfg.DBDSN); err != nil {
			return fmt.Errorf("run signer migrations: %w", err)
		}

		pool, err := openPool(ctx, cfg.DBDSN)
		if err != nil {
			return fmt.Errorf("connect to signer database: %w", err)
		}
		defer pool.Close()

		repo := repository.New(pool)
		store = sigstore.NewWithDB(repo)
		slog.Info("signature store: PostgreSQL persistence enabled")
	} else {
		// SIGNER_DB_DSN not set — fall back to in-memory store.
		// This MUST NOT reach production: records are lost on restart and replicas
		// have independent stores, breaking VerifyManifest for previously-signed images.
		slog.Warn("SIGNER_DB_DSN is not set — using in-memory signature store; " +
			"signature records will be lost on restart (SEC-015: set SIGNER_DB_DSN in production)")
		store = sigstore.New()
	}

	grpcHdl := handler.New(s, store)

	grpcOpts, err := buildGRPCOptions(cfg)
	if err != nil {
		return fmt.Errorf("build gRPC options: %w", err)
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
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
	// ReadHeaderTimeout prevents Slowloris attacks.
	// ReadTimeout and WriteTimeout bound the full request/response cycle.
	// SecureHeaders adds X-Content-Type-Options, X-Frame-Options to every response.
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpmiddleware.SecureHeaders(httpMux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// SEC-025: /metrics on a dedicated port so NetworkPolicy can allow Prometheus
	// to scrape :9090 without exposing the signer HTTP port to the cluster.
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
		slog.Info("shutting down signer service")
		grpcSrv.GracefulStop()
		_ = httpSrv.Shutdown(context.Background())
		_ = metricsSrv.Shutdown(context.Background())
		return nil
	case err := <-errCh:
		return err
	}
}

// runMigrations opens a temporary *sql.DB, applies all pending goose migrations
// from the embedded FS, then closes it. The connection pool is opened separately
// so migrations complete before the server begins serving traffic.
func runMigrations(dsn string) error {
	// Reject sslmode=disable to prevent cleartext password transmission.
	if strings.Contains(strings.ToLower(dsn), "sslmode=disable") {
		return fmt.Errorf("SIGNER_DB_DSN must not use sslmode=disable")
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()

	goose.SetBaseFS(signermigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("signer database migrations applied")
	return nil
}

// openPool creates and pings a pgxpool for the signer database.
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
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

// loadSigner constructs a Signer from config. Each backend is independently
// instantiable so a deployment can switch backends by changing one env var.
func loadSigner(cfg *config.Config) (signing.Signer, error) {
	switch cfg.SignerKeyBackend {
	case "env":
		return signing.NewEnv(cfg.CosignPrivateKeyB64, cfg.CosignPublicKeyB64)
	case "vault":
		return signing.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultCosignPath)
	case "awskms", "gcpkms", "azurekms":
		return nil, fmt.Errorf("SIGNER_KEY_BACKEND=%s is not yet implemented; use env or vault backend", cfg.SignerKeyBackend)
	default:
		return nil, fmt.Errorf("unknown SIGNER_KEY_BACKEND: %s", cfg.SignerKeyBackend)
	}
}
