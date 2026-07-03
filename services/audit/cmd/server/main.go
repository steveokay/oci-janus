// Command server is the entrypoint for the audit service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/steveokay/oci-janus/libs/config/loader"
	"github.com/steveokay/oci-janus/libs/crypto/rekey"
	"github.com/steveokay/oci-janus/libs/observability/otel"
	"github.com/steveokay/oci-janus/services/audit/internal/config"
	"github.com/steveokay/oci-janus/services/audit/internal/rotatekek"
	"github.com/steveokay/oci-janus/services/audit/internal/server"
)

func main() {
	// rotate-kek subcommand (RED-FU-015). Dispatched before config load so the
	// KEK rotation CLI does not require the full server environment.
	if len(os.Args) > 1 && os.Args[1] == "rotate-kek" {
		if err := rotatekek.Run(context.Background(), os.Args[2:], os.Stdout); err != nil {
			var verr *rekey.ValidationError
			if errors.As(err, &verr) {
				slog.Error("rotate-kek validation error", "err", err)
				os.Exit(2)
			}
			if errors.Is(err, rekey.ErrRowsRemain) {
				slog.Error("rotate-kek verify: rows remain on the old key", "err", err)
				os.Exit(3)
			}
			slog.Error("rotate-kek failed", "err", err)
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
