package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for registry-core.
type Config struct {
	LogLevel  string `mapstructure:"LOG_LEVEL"`
	LogFormat string `mapstructure:"LOG_FORMAT"`
	GRPCAddr  string `mapstructure:"GRPC_ADDR"`
	HTTPAddr  string `mapstructure:"HTTP_ADDR"`
	// MetricsAddr is the dedicated Prometheus scrape port (SEC-025).
	MetricsAddr string `mapstructure:"METRICS_ADDR"`

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	AuthGRPCAddr     string `mapstructure:"AUTH_GRPC_ADDR"`
	AuthRealm        string `mapstructure:"AUTH_REALM"`
	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`
	// SignerGRPCAddr (futures.md Tier 1 #3) wires the signed-image
	// admission gate. When empty, repos with `require_signature=true`
	// log a warning and ALLOW the pull rather than failing closed —
	// a dev-stack convenience so the registry boots without a running
	// signer service. Production deployments should always set this.
	SignerGRPCAddr string `mapstructure:"SIGNER_GRPC_ADDR"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`

	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// PullEventSampleRate (FE-API-042) sets the probability that a successful
	// manifest GET publishes a pull.image event. Range [0.0, 1.0]; default 1.0
	// (every pull). Reducing this loses FE-API-030 analytics precision
	// proportionally — but the FE-API-043 max_idle_days retention rule rides
	// services/metadata's 24h-debounced last_pulled_at update so its accuracy
	// is preserved as long as sample rate is > 0. Set to 0.0 to disable the
	// publish entirely (analytics returns zeros + max_idle_days stops working).
	PullEventSampleRate float64 `mapstructure:"PULL_EVENT_SAMPLE_RATE"`

	OTELExporter     string  `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint     string  `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName  string  `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment  string  `mapstructure:"OTEL_ENVIRONMENT"`
	OTELSamplingRate float64 `mapstructure:"OTEL_SAMPLING_RATE"`
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
	viper.SetDefault("GRPC_ADDR", ":50052")
	viper.SetDefault("HTTP_ADDR", ":8081")
	viper.SetDefault("METRICS_ADDR", ":9090")
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-core")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)
	viper.SetDefault("REDIS_ADDR", "localhost:6379")
	viper.SetDefault("AUTH_GRPC_ADDR", "registry-auth:50051")
	viper.SetDefault("AUTH_REALM", "http://localhost:8080/auth/token")
	viper.SetDefault("METADATA_GRPC_ADDR", "registry-metadata:50051")
	viper.SetDefault("STORAGE_GRPC_ADDR", "registry-storage:50051")
	// FE-API-042: every successful manifest GET publishes a pull.image event by default.
	viper.SetDefault("PULL_EVENT_SAMPLE_RATE", 1.0)

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	required := map[string]string{
		"RABBITMQ_URL": cfg.RabbitMQURL,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	// PENTEST-010: AUTH_REALM must be HTTPS in any non-development environment.
	// Docker clients send Basic-auth credentials to this realm, so an http://
	// realm in production would leak credentials over the network.
	if err := validateAuthRealm(cfg.AuthRealm, cfg.OTELEnvironment); err != nil {
		return err
	}
	// FE-API-042: refuse out-of-range sample rates at startup so a typo in the
	// helm values (e.g. "10" instead of "1.0") doesn't silently disable the
	// publish or burn CPU on a coin flip that's always heads.
	if cfg.PullEventSampleRate < 0.0 || cfg.PullEventSampleRate > 1.0 {
		return fmt.Errorf("PULL_EVENT_SAMPLE_RATE must be in [0.0, 1.0], got %v", cfg.PullEventSampleRate)
	}
	return nil
}

// validateAuthRealm enforces the PENTEST-010 contract: if the realm is HTTP,
// either OTEL_ENVIRONMENT is "development" (silently allowed) or we refuse to
// start. A startup warning is also emitted in dev so misconfigurations are
// visible in the logs.
func validateAuthRealm(realm, environment string) error {
	if realm == "" {
		return nil
	}
	u, err := url.Parse(realm)
	if err != nil {
		return fmt.Errorf("AUTH_REALM is not a valid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" {
		return nil
	}
	if scheme != "http" {
		return fmt.Errorf("AUTH_REALM scheme %q is not supported (use https://, or http:// only in development)", u.Scheme)
	}
	env := strings.ToLower(environment)
	if env == "production" || env == "staging" {
		return fmt.Errorf("AUTH_REALM must use HTTPS in %s (got %q) — Basic auth would be sent over plaintext", env, realm)
	}
	slog.Warn("AUTH_REALM uses HTTP — acceptable only for local development", "realm", realm, "environment", environment)
	return nil
}
