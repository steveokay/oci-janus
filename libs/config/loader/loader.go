// Package loader provides a Viper-based configuration loader used by every
// service in the registry platform. It defines shared config structs and a
// Load helper so each service's internal/config package doesn't repeat boilerplate.
package loader

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
	"google.golang.org/grpc/credentials"

	"github.com/steveokay/oci-janus/libs/auth/mtls"
)

// BaseConfig contains fields that every service needs.
// Each service embeds this struct in its own Config type.
type BaseConfig struct {
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	LogFormat string `mapstructure:"LOG_FORMAT"`

	// gRPC and HTTP listener addresses
	GRPCAddr string `mapstructure:"GRPC_ADDR"`
	HTTPAddr string `mapstructure:"HTTP_ADDR"`

	// MetricsAddr is the listen address for the dedicated Prometheus scrape server
	// (SEC-025). It runs on a separate port from the business HTTP server so that
	// Kubernetes NetworkPolicy can restrict Prometheus access to port 9090 without
	// affecting client-facing traffic. The metrics server is intentionally
	// unauthenticated — access is restricted by NetworkPolicy in production.
	MetricsAddr string `mapstructure:"METRICS_ADDR"`

	// mTLS certificate paths — required by all gRPC servers and clients
	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	// OpenTelemetry settings
	OTELExporter     string  `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint     string  `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName  string  `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment  string  `mapstructure:"OTEL_ENVIRONMENT"`
	OTELSamplingRate float64 `mapstructure:"OTEL_SAMPLING_RATE"`
}

// DBConfig contains PostgreSQL connection and pool settings.
// Only services that own a database (auth, metadata, tenant, audit) embed this.
// Pool defaults implement REM-006: prevent exhaustion and map acquire timeouts
// to codes.ResourceExhausted rather than hanging forever.
type DBConfig struct {
	DBDSN        string `mapstructure:"DB_DSN"`
	DBDSNReplica string `mapstructure:"DB_DSN_REPLICA"` // optional read replica

	// Pool sizing — tune via env; defaults are safe for most workloads
	DBMaxConns int32 `mapstructure:"DB_MAX_CONNS"`
	DBMinConns int32 `mapstructure:"DB_MIN_CONNS"`

	// Timeouts — kept short so pool exhaustion surfaces as ResourceExhausted quickly
	DBConnectTimeout  time.Duration `mapstructure:"DB_CONNECT_TIMEOUT"`
	DBMaxConnLifetime time.Duration `mapstructure:"DB_MAX_CONN_LIFETIME"`
	DBMaxConnIdleTime time.Duration `mapstructure:"DB_MAX_CONN_IDLE_TIME"`

	// Environment is consulted by PoolConfig to gate PENTEST-017's dev-default
	// credential check: in production/staging, a DSN that embeds a known dev
	// password ("registry", "postgres") refuses to start. Wire from
	// BaseConfig.OTELEnvironment in the service's Load function.
	Environment string `mapstructure:"OTEL_ENVIRONMENT"`
}

// PoolConfig constructs a pgxpool.Config from DBConfig ready for pgxpool.NewWithConfig.
// Enforces that DB_DSN includes sslmode=require — sslmode=disable is rejected at startup.
// A warning is emitted (but startup is not blocked) when sslmode is weaker than "require",
// because sslmode=prefer is acceptable in local dev compose but must not reach production.
func (c *DBConfig) PoolConfig() (*pgxpool.Config, error) {
	if c.DBDSN == "" {
		return nil, fmt.Errorf("DB_DSN is required")
	}
	// sslmode=disable would silently transmit passwords in cleartext
	dsn := strings.ToLower(c.DBDSN)
	if strings.Contains(dsn, "sslmode=disable") || !strings.Contains(dsn, "sslmode=") {
		return nil, fmt.Errorf("DB_DSN must include sslmode=require; sslmode=disable is not permitted")
	}

	// Extract the sslmode value from the DSN to check for weaker modes.
	// sslmode=prefer silently falls back to plaintext when the server has no cert —
	// this is the case in dev compose where postgres runs without TLS.
	// Production DSNs must always use sslmode=require.
	sslMode := extractSSLMode(dsn)
	if sslMode != "require" {
		// Warn when SSL mode is weaker than require. sslmode=prefer silently falls back
		// to plaintext when the server has no cert — this is the case in dev compose.
		// Production DSNs must always use sslmode=require.
		slog.Warn("DB DSN uses non-enforcing SSL mode — connections may be unencrypted",
			"ssl_mode", sslMode,
			"recommendation", "use sslmode=require in production")
	}

	// PENTEST-017: refuse to start if the DSN carries a known dev-default
	// password in production/staging. Warns in development. No-op when the
	// DSN does not embed credentials (e.g. trust auth or IAM auth).
	if err := CheckDevDefaultsFromDSN(c.Environment, "POSTGRES_PASSWORD", c.DBDSN); err != nil {
		return nil, err
	}

	cfg, err := pgxpool.ParseConfig(c.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("parse DB_DSN: %w", err)
	}
	if c.DBMaxConns > 0 {
		cfg.MaxConns = c.DBMaxConns
	}
	if c.DBMinConns > 0 {
		cfg.MinConns = c.DBMinConns
	}
	if c.DBConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = c.DBConnectTimeout
	}
	if c.DBMaxConnLifetime > 0 {
		cfg.MaxConnLifetime = c.DBMaxConnLifetime
	}
	if c.DBMaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = c.DBMaxConnIdleTime
	}
	return cfg, nil
}

// extractSSLMode parses the sslmode query parameter out of a lowercased DSN string.
// It handles both URL-style DSNs (sslmode=X in query string) and key=value DSNs.
// Returns "unknown" when sslmode cannot be determined — callers should treat that
// conservatively and warn rather than block startup.
func extractSSLMode(dsn string) string {
	// Look for "sslmode=<value>" — works for both URL and key=value DSN formats.
	const prefix = "sslmode="
	idx := strings.Index(dsn, prefix)
	if idx < 0 {
		return "unknown"
	}
	rest := dsn[idx+len(prefix):]
	// Value ends at next '&', ' ', or end of string
	for i, ch := range rest {
		if ch == '&' || ch == ' ' || ch == ';' {
			return rest[:i]
		}
	}
	return rest
}

// ReplicaPoolConfig constructs a pgxpool.Config for the optional read replica.
// Returns an error if DB_DSN_REPLICA is empty or invalid.
func (c *DBConfig) ReplicaPoolConfig() (*pgxpool.Config, error) {
	if c.DBDSNReplica == "" {
		return nil, fmt.Errorf("DB_DSN_REPLICA is not set")
	}
	// Reuse PoolConfig logic by temporarily swapping the DSN.
	primary := c.DBDSN
	c.DBDSN = c.DBDSNReplica
	cfg, err := c.PoolConfig()
	c.DBDSN = primary
	if err != nil {
		return nil, fmt.Errorf("replica pool config: %w", err)
	}
	return cfg, nil
}

// Load binds environment variables into cfg using Viper and applies
// service-agnostic defaults. cfg must be a pointer to a mapstructure-tagged struct.
// serviceName sets the default for OTEL_SERVICE_NAME when the env var is absent.
func Load(serviceName string, cfg any) error {
	v := viper.New()
	v.AutomaticEnv()

	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "json")
	v.SetDefault("GRPC_ADDR", ":50051")
	v.SetDefault("HTTP_ADDR", ":8080")
	// METRICS_ADDR is the dedicated Prometheus scrape port (SEC-025).
	// Separated from HTTP_ADDR so NetworkPolicy can allow Prometheus on :9090
	// without opening the business port to Prometheus pods.
	v.SetDefault("METRICS_ADDR", ":9090")
	v.SetDefault("OTEL_SERVICE_NAME", serviceName)
	v.SetDefault("OTEL_SAMPLING_RATE", 1.0)

	// DB pool defaults — these match the REM-006 recommended values
	v.SetDefault("DB_MAX_CONNS", 20)
	v.SetDefault("DB_MIN_CONNS", 2)
	v.SetDefault("DB_CONNECT_TIMEOUT", "5s")
	v.SetDefault("DB_MAX_CONN_LIFETIME", "30m")
	v.SetDefault("DB_MAX_CONN_IDLE_TIME", "5m")

	// Seed Viper with all environment variables so that AutomaticEnv works
	// correctly with Unmarshal. Viper's AllSettings (used by Unmarshal) only
	// returns keys it already knows; without this, fields with no SetDefault
	// entry (e.g. DB_DSN, REDIS_ADDR) are silently left empty.
	for _, e := range os.Environ() {
		k, val, ok := strings.Cut(e, "=")
		if ok {
			v.Set(k, val)
		}
	}

	if err := v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}
	return nil
}

// DeploymentMode describes how this binary is deployed.
// "single" — one tenant per deployment, auto-bootstrapped, FE hides tenant chrome.
// "multi"  — multi-tenant capability enabled, FE renders tenant switcher / admin.
type DeploymentMode string

const (
	// DeploymentModeSingle is the default deployment mode for self-hosted OSS installations.
	// One tenant per deployment, auto-bootstrapped.
	DeploymentModeSingle DeploymentMode = "single"

	// DeploymentModeMulti enables multi-tenant capability.
	// FE renders tenant switcher / admin panels.
	DeploymentModeMulti DeploymentMode = "multi"
)

// LoadDeploymentMode reads DEPLOYMENT_MODE env var.
// Defaults to "single" (the OSS self-hosted default).
// Returns an error for unknown values so misconfiguration fails loudly at startup.
func LoadDeploymentMode() (DeploymentMode, error) {
	v := strings.TrimSpace(os.Getenv("DEPLOYMENT_MODE"))
	if v == "" {
		return DeploymentModeSingle, nil
	}
	switch DeploymentMode(v) {
	case DeploymentModeSingle, DeploymentModeMulti:
		return DeploymentMode(v), nil
	default:
		return "", fmt.Errorf("invalid DEPLOYMENT_MODE %q: must be 'single' or 'multi'", v)
	}
}

// RequireFields returns an error listing the names of any required config fields
// whose values are empty. Pass a map of env-var-name → current-value pairs.
//
//	loader.RequireFields(map[string]string{
//	    "MTLS_CA_CERT_PATH": cfg.MTLSCACertPath,
//	    "JWT_PRIVATE_KEY":   cfg.JWTPrivateKey,
//	})
func RequireFields(fields map[string]string) error {
	var missing []string
	for name, value := range fields {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required env vars not set: %v", missing)
}

// MTLSClientCreds is a one-liner wrapper around libs/auth/mtls.ClientCreds
// for services whose Config embeds BaseConfig. It threads the three mTLS
// path fields and the remote service name through so call sites become:
//
//	creds, err := cfg.MTLSClientCreds("registry-tenant")
//	if err != nil { return ... }
//	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
//
// REDESIGN-001 RED-FU-012 — the unification follow-up flagged by
// code-review-agent on SEC-039 (PR #182). The same `mtls.ClientCreds(
// cfg.MTLSCACertPath, cfg.MTLSCertPath, cfg.MTLSKeyPath, name)` line
// previously lived as `clientCreds(cfg, name)` / `buildClientCreds(cfg,
// name)` / `buildGRPCCreds(cfg, name)` per service. Centralising here
// keeps the empty-serverName regression (SEC-038 / SEC-039) impossible
// to re-introduce because every BaseConfig consumer goes through the
// same code path.
//
// Returns insecure.NewCredentials when ALL three cert paths are empty
// (dev posture); with any path set, propagates the TLS load error so a
// corrupted cert fails-loud instead of silently downgrading to plaintext.
func (c *BaseConfig) MTLSClientCreds(serverName string) (credentials.TransportCredentials, error) {
	return mtls.ClientCreds(c.MTLSCACertPath, c.MTLSCertPath, c.MTLSKeyPath, serverName)
}

// MTLSConfig is the shared mTLS configuration block.
// Every service constructs this via LoadMTLSConfig() in main.go.
type MTLSConfig struct {
	// Required controls whether mTLS certificates must be present.
	// Production deployments set this to true; dev deployments set it to false.
	Required   bool
	CACertPath string
	CertPath   string
	KeyPath    string
}

// LoadMTLSConfig reads MTLS_* env vars.
// MTLS_REQUIRED defaults to "true" (fail-safe, production-safe).
// Set MTLS_REQUIRED=false in local dev to skip cert enforcement.
func LoadMTLSConfig() MTLSConfig {
	return MTLSConfig{
		Required:   strings.ToLower(os.Getenv("MTLS_REQUIRED")) != "false",
		CACertPath: os.Getenv("MTLS_CA_CERT_PATH"),
		CertPath:   os.Getenv("MTLS_CERT_PATH"),
		KeyPath:    os.Getenv("MTLS_KEY_PATH"),
	}
}

// ValidateMTLSConfig fails if MTLS is required but any cert path is empty.
// Centralised here so adding a new service inherits the check automatically.
// This replaces ad-hoc per-service validation (review §A3).
func ValidateMTLSConfig(cfg MTLSConfig) error {
	if !cfg.Required {
		return nil
	}
	missing := []string{}
	if cfg.CACertPath == "" {
		missing = append(missing, "MTLS_CA_CERT_PATH")
	}
	if cfg.CertPath == "" {
		missing = append(missing, "MTLS_CERT_PATH")
	}
	if cfg.KeyPath == "" {
		missing = append(missing, "MTLS_KEY_PATH")
	}
	if len(missing) > 0 {
		return fmt.Errorf("MTLS_REQUIRED=true but missing: %s", strings.Join(missing, ", "))
	}
	return nil
}
