// Command server is the entrypoint for the registry-mcp service.
//
// This binary exposes the OCI-Janus registry as a Model Context Protocol
// (MCP) server so AI coding assistants (Claude Desktop, Cursor,
// continue.dev, Copilot Workspace) can query it in natural language.
//
// Load-bearing detail for stdio transport: EVERY slog output is routed
// to os.Stderr, NEVER stdout. Claude Desktop's stdio transport treats
// stdout as MCP JSON-RPC frames — any stray write there breaks the
// client with an "invalid JSON" parse error and Claude drops the
// connection.
//
// FUT-031. Read-only tools only in v1; mutating tools land in Wave 2
// with explicit consent UX.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/steveokay/oci-janus/services/mcp/internal/client"
	"github.com/steveokay/oci-janus/services/mcp/internal/config"
	"github.com/steveokay/oci-janus/services/mcp/internal/tools"
)

func main() {
	// Signal handling for both transports. Stdio typically exits when
	// the client closes stdin, but SIGTERM handling makes the compose
	// service's `docker stop` clean rather than a 10-second SIGKILL.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		// Config load is the ONLY place we log to stderr *before*
		// setupLogger runs — we can't know the LogFormat yet.
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg.LogFormat, cfg.LogLevel)
	logger.Info("registry-mcp starting",
		"transport", cfg.Transport,
		"management_url", cfg.ManagementURL,
		"tenant_id", cfg.TenantID,
		// Deliberately NOT logging the API key — even at debug level.
	)

	reg := client.NewRegistry(cfg.ManagementURL, cfg.APIKey, cfg.TenantID)
	// Seed the tool registry with the API-key sentinel so any error
	// path that echoes the upstream response gets scrubbed before
	// the LLM sees it. See tools.Registry.errorResult.
	diag := tools.DeploymentInfo{
		ManagementURL: cfg.ManagementURL,
		TenantID:      cfg.TenantID,
		Transport:     cfg.Transport,
	}
	toolReg := tools.NewRegistry(reg, logger, []string{cfg.APIKey}, diag)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "oci-janus-registry",
		Title:   "OCI-Janus Registry",
		Version: tools.Version,
	}, nil)
	toolReg.Register(server)

	switch cfg.Transport {
	case config.TransportStdio:
		if err := runStdio(ctx, server, logger); err != nil {
			logger.Error("stdio transport exited with error", "err", err)
			os.Exit(1)
		}
	case config.TransportHTTP:
		if err := runHTTP(ctx, server, cfg.HTTPAddr, logger); err != nil {
			logger.Error("http transport exited with error", "err", err)
			os.Exit(1)
		}
	default:
		logger.Error("unknown transport", "transport", cfg.Transport)
		os.Exit(1)
	}
	logger.Info("registry-mcp exited cleanly")
}

// runStdio wires the MCP server to os.Stdin + os.Stdout via the SDK's
// StdioTransport. server.Run blocks until the client disconnects or
// ctx is cancelled.
func runStdio(ctx context.Context, s *mcp.Server, logger *slog.Logger) error {
	logger.Info("stdio transport listening on stdin/stdout")
	return s.Run(ctx, &mcp.StdioTransport{})
}

// runHTTP wires the MCP server to a standard *http.Server serving the
// SDK's streamable-HTTP handler. Suitable for Cursor remote,
// continue.dev, and any HTTP-first MCP client.
//
// getServer returns the same *mcp.Server for every request — this is
// a single-tenant MCP surface (the tenant is pinned via config, not
// per-session), so there's no reason to spin up a per-request server.
func runHTTP(ctx context.Context, s *mcp.Server, addr string, logger *slog.Logger) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{
			// Default localhost-only protection is fine — the compose
			// service binds :8087 behind a network policy that only
			// allows the frontend / dashboard reverse-proxy through.
		},
	)
	// ReadHeaderTimeout guards against Slowloris (SEC-019/020) — the
	// same posture other HTTP-serving services in this repo use.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	// Graceful shutdown on ctx cancel.
	go func() {
		<-ctx.Done()
		// Give in-flight requests 5 seconds to drain.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("http server shutdown", "err", err)
		}
	}()

	logger.Info("http transport listening", "addr", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// setupLogger returns a *slog.Logger writing to os.Stderr. Load-bearing:
// the writer is Stderr on BOTH transport paths so stdio JSON-RPC frames
// don't get interleaved with log lines. Every "log to stdout" pattern
// from other services in this repo is deliberately NOT copied here.
func setupLogger(format, level string) *slog.Logger {
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
	if format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	logger := slog.New(h)
	// Set slog default too so any transitive package (e.g. the SDK)
	// that grabs slog.Default() picks up the stderr sink.
	slog.SetDefault(logger)
	return logger
}
