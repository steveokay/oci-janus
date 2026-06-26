// Command server is the entrypoint for the registry-auth service.
// It also dispatches the `bootstrap` subcommand (REDESIGN-001 Phase 3.1.b)
// when os.Args[1] == "bootstrap" — before loading the full server config.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/steveokay/oci-janus/libs/config/loader"
	"github.com/steveokay/oci-janus/libs/observability/otel"
	"github.com/steveokay/oci-janus/services/auth/internal/bootstrap"
	"github.com/steveokay/oci-janus/services/auth/internal/config"
	"github.com/steveokay/oci-janus/services/auth/internal/server"
)

func main() {
	// ── Bootstrap subcommand dispatch ─────────────────────────────────────────
	// Must happen BEFORE config.Load so the bootstrap CLI does not fail due to
	// missing server-only env vars (OTEL_EXPORTER, MTLS_CA_CERT_PATH, etc.).
	// The bootstrap subcommand has its own minimal config (AUTH_DB_DSN,
	// TENANT_DB_DSN, DEPLOYMENT_MODE) loaded inside bootstrap.Run.
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		if err := bootstrap.Run(context.Background(), os.Args[2:], os.Stdin, os.Stdout); err != nil {
			var verr *bootstrap.ValidationError
			if errors.As(err, &verr) {
				// Operator input error → exit 2 so callers can distinguish from
				// infrastructure failures (exit 1).
				slog.Error("bootstrap validation error", "err", err)
				os.Exit(2)
			}
			slog.Error("bootstrap failed", "err", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogFormat, cfg.LogLevel)

	// mTLS configuration validation — fails loudly if MTLS_REQUIRED=true and any
	// path is empty. Centralised in libs/config/loader so every service inherits
	// the production-safe check (Review §A3, REDESIGN-001 Phase 1.3).
	mtlsCfg := loader.LoadMTLSConfig()
	if err := loader.ValidateMTLSConfig(mtlsCfg); err != nil {
		slog.Error("mTLS configuration invalid", "err", err)
		os.Exit(1)
	}

	shutdown, err := otel.Bootstrap(ctx, otel.Config{
		Exporter:     cfg.OTELExporter,
		Endpoint:     cfg.OTELEndpoint,
		ServiceName:  cfg.OTELServiceName,
		Environment:  cfg.OTELEnvironment,
		SamplingRate: cfg.OTELSamplingRate,
	})
	if err != nil {
		slog.Error("failed to bootstrap OTEL", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			slog.Error("OTEL shutdown error", "err", err)
		}
	}()

	if err := server.Run(ctx, cfg); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

func setupLogger(format, level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}
