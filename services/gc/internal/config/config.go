package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for the GC service.
type Config struct {
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	LogFormat   string `mapstructure:"LOG_FORMAT"`
	GRPCAddr    string `mapstructure:"GRPC_ADDR"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`
	// MetricsAddr is the dedicated Prometheus scrape port (SEC-025).
	MetricsAddr string `mapstructure:"METRICS_ADDR"`

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	OTELExporter     string  `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint     string  `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName  string  `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment  string  `mapstructure:"OTEL_ENVIRONMENT"`
	OTELSamplingRate float64 `mapstructure:"OTEL_SAMPLING_RATE"`

	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`
	RabbitMQURL      string `mapstructure:"RABBITMQ_URL"`

	// GCAdvisoryLockDBDSN is a PostgreSQL DSN used solely for pg_try_advisory_lock
	// coordination. Optional — if unset, advisory locking is disabled (safe for
	// single-worker deployments).
	GCAdvisoryLockDBDSN string `mapstructure:"GC_ADVISORY_LOCK_DB_DSN"`

	// DBDSN is the gc service's own Postgres DSN (FE-API-032). When set,
	// the service runs goose migrations on startup and persists every
	// sweep to the gc_runs table. When empty the gc service falls back
	// to its pre-FE-API-032 behaviour: scheduled sweeps still run, but
	// no history is recorded and the gRPC GCService surface refuses
	// every call with FailedPrecondition.
	DBDSN string `mapstructure:"DB_DSN"`
	// DBMaxConns caps the connection pool size. Defaults to 10 (the
	// gc service issues short, bounded queries — far below the
	// platform default of 20 used by registry-metadata). Typed as
	// int32 to line up with libs/config/loader.DBConfig.DBMaxConns.
	DBMaxConns int32 `mapstructure:"DB_MAX_CONNS"`

	// GCMode controls what the collector deletes: dry-run | manifests | blobs | full.
	GCMode string `mapstructure:"GC_MODE"`
	// GCRunIntervalHours is the number of hours between automatic GC runs.
	GCRunIntervalHours int `mapstructure:"GC_RUN_INTERVAL_HOURS"`
	// BlobMinAgeHours guards against deleting blobs that belong to an in-flight push
	// (blobs are written before manifests; a very fresh blob may have no manifest link yet).
	BlobMinAgeHours int `mapstructure:"GC_BLOB_MIN_AGE_HOURS"`
	// ManifestMinAgeHours prevents deleting manifests pushed moments before a tag is attached.
	ManifestMinAgeHours int `mapstructure:"GC_MANIFEST_MIN_AGE_HOURS"`

	// ─── FE-API-040: retention executor ──────────────────────────────────────
	//
	// RetentionGraceDays is the soft-delete window before retention_grace mode
	// hard-deletes a manifest. Defaults to 7 days — long enough to recover
	// from an accidental retention policy via the ClearManifestRetentionPending
	// UI affordance, short enough that forgetting about the window doesn't
	// leak storage indefinitely.
	RetentionGraceDays int `mapstructure:"RETENTION_GRACE_DAYS"`
	// RetentionGraceIntervalHours is the cadence at which the cross-tenant
	// grace ticker fires a retention_grace sweep. Defaults to 6h — a sweet
	// spot between "operator sees the grace window count down clearly" and
	// "we're not putting unbounded pressure on the manifests scan".
	RetentionGraceIntervalHours int `mapstructure:"RETENTION_GRACE_INTERVAL_HOURS"`
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	viper.AutomaticEnv()
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			viper.Set(k, v)
		}
	}
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "json")
	viper.SetDefault("GRPC_ADDR", ":50051")
	viper.SetDefault("HTTP_ADDR", ":8080")
	viper.SetDefault("METRICS_ADDR", ":9090")
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-gc")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)
	viper.SetDefault("GC_MODE", "full")
	viper.SetDefault("GC_RUN_INTERVAL_HOURS", 24)
	viper.SetDefault("GC_BLOB_MIN_AGE_HOURS", 1)
	viper.SetDefault("GC_MANIFEST_MIN_AGE_HOURS", 24)
	viper.SetDefault("DB_MAX_CONNS", 10)
	// FE-API-040 retention executor defaults.
	viper.SetDefault("RETENTION_GRACE_DAYS", 7)
	viper.SetDefault("RETENTION_GRACE_INTERVAL_HOURS", 6)

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// validate returns an error for any missing required field or invalid enum value.
func validate(cfg *Config) error {
	// MTLS_* paths are optional — server.go falls back to insecure with a warning
	// when they are absent (development mode only).
	required := map[string]string{
		"METADATA_GRPC_ADDR": cfg.MetadataGRPCAddr,
		"STORAGE_GRPC_ADDR":  cfg.StorageGRPCAddr,
		"RABBITMQ_URL":       cfg.RabbitMQURL,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	validModes := map[string]bool{"dry-run": true, "manifests": true, "blobs": true, "full": true}
	if !validModes[cfg.GCMode] {
		return fmt.Errorf("GC_MODE must be one of: dry-run, manifests, blobs, full")
	}
	return nil
}
