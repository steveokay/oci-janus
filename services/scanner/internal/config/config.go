// Package config loads runtime configuration for registry-scanner.
//
// Until FE-API-018 the scanner had no DB of its own. Scan policies (FE-API-018)
// and compliance reports (FE-API-019) require durable per-tenant state, so the
// service now owns a small Postgres schema and reads DB_DSN from the
// environment alongside its existing RabbitMQ + plugin configuration.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config holds all runtime configuration for the scanner service.
//
// RED-FU-014 — the standard BaseConfig fields (LogLevel/LogFormat/
// GRPCAddr/HTTPAddr/MetricsAddr/MTLS_*/OTEL_*) are inherited via the
// squashed loader.BaseConfig embed so they live in one canonical place
// and gain the cfg.MTLSClientCreds(serverName) method from RED-FU-012.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`

	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`

	PluginPath     string `mapstructure:"SCANNER_PLUGIN_PATH"`
	PluginChecksum string `mapstructure:"SCANNER_PLUGIN_CHECKSUM"`

	WorkerCount    int `mapstructure:"SCANNER_WORKER_COUNT"`
	JobTimeoutSecs int `mapstructure:"SCANNER_JOB_TIMEOUT_SECS"`

	// DBDSN is the Postgres connection string for the scanner's own DB
	// (FE-API-018 scan policies + FE-API-019 compliance reports). Required.
	DBDSN string `mapstructure:"DB_DSN"`
	// DBMaxConns caps the pool size; default 20 matches the platform
	// convention used by every other service.
	DBMaxConns int32 `mapstructure:"DB_MAX_CONNS"`

	// ReportOutputDir is the on-disk directory the compliance-report
	// background worker writes PDF + SPDX JSON outputs to. Defaults to
	// /tmp/reports — production deployments should swap this for object
	// storage and front the download routes with signed URLs.
	ReportOutputDir string `mapstructure:"REPORT_OUTPUT_DIR"`

	// ReportPollIntervalSecs is how often the compliance-report worker
	// scans for pending jobs. Five seconds gives a generous safety margin
	// for tests + dev seeds; production may want shorter.
	ReportPollIntervalSecs int `mapstructure:"REPORT_POLL_INTERVAL_SECS"`

	// REM-011 Phase 2 — RunTestScan fixture.
	//
	// RunTestScan exercises the active adapter end-to-end against a
	// pre-determined repo+tag pair on a real tenant. The dev compose
	// stack seeds dev/alpine:latest under the dev tenant, so those are
	// the defaults baked into the binary. Production deployments should
	// override these to point at an in-house "scan canary" image so the
	// admin UI's "run test scan" button continues to work without
	// leaning on the dev seed data.
	TestScanTenantID    string `mapstructure:"SCANNER_TEST_TENANT_ID"`
	TestScanRepository  string `mapstructure:"SCANNER_TEST_REPOSITORY"`
	TestScanManifestRef string `mapstructure:"SCANNER_TEST_MANIFEST_REF"`

	// REDESIGN-001 Phase 3.4 — tenant gRPC client for SingleTenantInjector.
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`

	// DeploymentMode is the binary's posture, normalised by
	// libs/config/loader.LoadDeploymentMode. Empty env defaults to single.
	DeploymentMode loader.DeploymentMode `mapstructure:"-"`
}

// Load reads configuration from environment variables and validates required fields.
//
// Viper's AutomaticEnv alone is not enough: viper.Unmarshal walks the
// struct via mapstructure tags but only finds keys that have been
// previously Set or BindEnv'd. Without that step, env vars come through
// as empty even though they're present in os.Environ. We explicitly
// promote every env var into viper's key space — same pattern the gc
// and audit services use — so Unmarshal sees everything.
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-scanner")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)
	viper.SetDefault("SCANNER_WORKER_COUNT", 4)
	viper.SetDefault("SCANNER_JOB_TIMEOUT_SECS", 600)
	viper.SetDefault("DB_MAX_CONNS", 20)
	viper.SetDefault("REPORT_OUTPUT_DIR", "/tmp/reports")
	viper.SetDefault("REPORT_POLL_INTERVAL_SECS", 5)
	// Dev defaults — the dev-compose seed publishes dev/alpine:latest
	// under this tenant ID. Production deployments must override all
	// three to a real "scan canary" image.
	viper.SetDefault("SCANNER_TEST_TENANT_ID", "98dbe36b-ef28-4903-b25c-bff1b2921c9e")
	viper.SetDefault("SCANNER_TEST_REPOSITORY", "dev/alpine")
	viper.SetDefault("SCANNER_TEST_MANIFEST_REF", "latest")

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	// REDESIGN-001 Phase 3.4 — read DEPLOYMENT_MODE via the typed helper.
	mode, err := loader.LoadDeploymentMode()
	if err != nil {
		return nil, fmt.Errorf("load deployment mode: %w", err)
	}
	cfg.DeploymentMode = mode
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	required := map[string]string{
		"MTLS_CA_CERT_PATH":       cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":          cfg.MTLSCertPath,
		"MTLS_KEY_PATH":           cfg.MTLSKeyPath,
		"RABBITMQ_URL":            cfg.RabbitMQURL,
		"METADATA_GRPC_ADDR":      cfg.MetadataGRPCAddr,
		"STORAGE_GRPC_ADDR":       cfg.StorageGRPCAddr,
		"SCANNER_PLUGIN_PATH":     cfg.PluginPath,
		"SCANNER_PLUGIN_CHECKSUM": cfg.PluginChecksum,
		"DB_DSN":                  cfg.DBDSN,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
