// Package config loads the MCP server's runtime environment variables
// into a typed Config and validates them at startup. The MCP surface is
// a thin JSON-RPC wrapper on top of the management BFF HTTP API, so the
// env surface is deliberately tiny — no database, no RabbitMQ, no mTLS.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// TransportStdio is the MCP transport used by Claude Desktop / Cursor
// stdio-mode clients. The binary reads JSON-RPC frames from stdin and
// writes them to stdout — no other output may go to stdout.
const TransportStdio = "stdio"

// TransportHTTP is the MCP transport used by Cursor remote / continue.dev
// / any HTTP-first client. The binary serves the SDK's streamable HTTP
// handler on MCP_HTTP_ADDR.
const TransportHTTP = "http"

// Config is the complete MCP server env surface. Every field is env-driven
// so the compose service and Claude Desktop MCP config can wire the same
// binary through env vars alone.
type Config struct {
	// LogLevel + LogFormat mirror the platform-wide convention. Stdio
	// mode routes slog output to STDERR (never stdout) so protocol
	// frames stay parseable — see cmd/server/main.go for the wiring.
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	LogFormat string `mapstructure:"LOG_FORMAT"`

	// Transport picks between the two MCP transports the SDK supports.
	// Default: "stdio". "http" additionally requires HTTPAddr.
	Transport string `mapstructure:"MCP_TRANSPORT"`

	// HTTPAddr is the listen address when Transport=="http".
	HTTPAddr string `mapstructure:"MCP_HTTP_ADDR"`

	// ManagementURL is the base URL of the registry-management BFF.
	// Every tool call proxies through this URL — the tool surface stays
	// honest to what an operator could do in the dashboard.
	ManagementURL string `mapstructure:"MCP_MANAGEMENT_URL"`

	// APIKey is the service-account API key the MCP server uses to auth
	// to the management BFF. Format: "key.<uuid>.<64-hex-secret>"
	// (FUT-006 bearer form). Provisioned once by the operator via
	// /api-keys with read scopes. Never logged, never returned to the
	// LLM — see the tests in internal/tools that assert this.
	APIKey string `mapstructure:"MCP_API_KEY"`

	// TenantID pins the tenant whose data the MCP surface exposes.
	// UUID string. In single-mode deployments this is the bootstrap
	// tenant id; in multi-mode it's the workspace the operator wants
	// Claude to reason about.
	TenantID string `mapstructure:"MCP_TENANT_ID"`
}

// apiKeyRegex validates FUT-006 bearer form. The regex is intentionally
// strict — a malformed key would fail at the BFF's ValidateAPIKey call
// anyway, but failing at config-load gives the operator a clean error at
// container start rather than a delayed auth failure at first tool call.
var apiKeyRegex = regexp.MustCompile(`^key\.[0-9a-fA-F-]{36}\.[0-9a-fA-F]{64}$`)

// Load reads env vars via Viper and returns a validated *Config.
// Errors surface enough context to fix a misconfig without leaking the
// value of any secret env var (LOG_LEVEL/etc. is fine to include; API
// key IS NOT).
func Load() (*Config, error) {
	v := viper.New()
	v.AutomaticEnv()
	for _, e := range os.Environ() {
		if k, val, ok := strings.Cut(e, "="); ok {
			v.Set(k, val)
		}
	}
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "json")
	v.SetDefault("MCP_TRANSPORT", TransportStdio)
	v.SetDefault("MCP_HTTP_ADDR", ":8087")

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate enforces the invariants documented on each Config field.
func validate(cfg *Config) error {
	if cfg.ManagementURL == "" {
		return fmt.Errorf("MCP_MANAGEMENT_URL is required")
	}
	if !strings.HasPrefix(cfg.ManagementURL, "http://") && !strings.HasPrefix(cfg.ManagementURL, "https://") {
		return fmt.Errorf("MCP_MANAGEMENT_URL must be an http:// or https:// URL")
	}
	if cfg.APIKey == "" {
		// Deliberately do NOT include the (missing) value in the error.
		return fmt.Errorf("MCP_API_KEY is required")
	}
	if !apiKeyRegex.MatchString(cfg.APIKey) {
		// Return a shape-only error — never include the actual key.
		return fmt.Errorf("MCP_API_KEY is malformed (expected key.<uuid>.<64-hex-secret>)")
	}
	if cfg.TenantID == "" {
		return fmt.Errorf("MCP_TENANT_ID is required")
	}
	if _, err := uuid.Parse(cfg.TenantID); err != nil {
		return fmt.Errorf("MCP_TENANT_ID must be a valid UUID: %w", err)
	}
	switch cfg.Transport {
	case TransportStdio:
		// OK — no additional fields required.
	case TransportHTTP:
		if cfg.HTTPAddr == "" {
			return fmt.Errorf("MCP_HTTP_ADDR is required when MCP_TRANSPORT=http")
		}
	default:
		return fmt.Errorf("MCP_TRANSPORT must be %q or %q (got %q)", TransportStdio, TransportHTTP, cfg.Transport)
	}
	return nil
}
