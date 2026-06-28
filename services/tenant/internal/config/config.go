package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config holds all runtime configuration for the tenant service.
type Config struct {
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	LogFormat   string `mapstructure:"LOG_FORMAT"`
	GRPCAddr    string `mapstructure:"GRPC_ADDR"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`
	MetricsAddr string `mapstructure:"METRICS_ADDR"`

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	OTELExporter     string  `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint     string  `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName  string  `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment  string  `mapstructure:"OTEL_ENVIRONMENT"`
	OTELSamplingRate float64 `mapstructure:"OTEL_SAMPLING_RATE"`

	DBDSN      string `mapstructure:"DB_DSN"`
	DBMaxConns int32  `mapstructure:"DB_MAX_CONNS"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`

	// PlatformBaseDomain is the wildcard zone every tenant gets a registry
	// hostname under (`<slug>.<PlatformBaseDomain>`). Used by handler.GetTenant
	// to build the fallback host when no verified primary custom domain exists.
	// Defaults to `registry.localhost` for local dev.
	PlatformBaseDomain string `mapstructure:"PLATFORM_BASE_DOMAIN"`

	// DeploymentMode controls whether this binary is the OSS self-hosted
	// single-tenant default ("single") or the SaaS multi-tenant capability
	// ("multi"). Populated via libs/config/loader.LoadDeploymentMode in Load();
	// CreateTenant gates a second tenant insertion on this value
	// (REDESIGN-001 Phase 3.2 / Q-001 — hard error).
	DeploymentMode loader.DeploymentMode
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-tenant")
	viper.SetDefault("DB_MAX_CONNS", 20)
	viper.SetDefault("REDIS_ADDR", "redis:6379")
	viper.SetDefault("METRICS_ADDR", ":9090")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)
	// FE-API-007: the wildcard hostname zone used to derive fallback hosts
	// like `<slug>.registry.localhost`. Operators override per environment.
	viper.SetDefault("PLATFORM_BASE_DOMAIN", "registry.localhost")

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// DEPLOYMENT_MODE is read separately because the loader rejects unknown
	// values; viper would silently accept anything. Phase 3.2 needs this
	// to gate CreateTenant from accepting a second tenant.
	mode, err := loader.LoadDeploymentMode()
	if err != nil {
		return nil, fmt.Errorf("invalid DEPLOYMENT_MODE: %w", err)
	}
	cfg.DeploymentMode = mode

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	required := map[string]string{
		"MTLS_CA_CERT_PATH": cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":    cfg.MTLSCertPath,
		"MTLS_KEY_PATH":     cfg.MTLSKeyPath,
		"DB_DSN":            cfg.DBDSN,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
