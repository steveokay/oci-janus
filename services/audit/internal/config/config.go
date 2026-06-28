package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for the audit service.
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

	DBDSN            string `mapstructure:"DB_DSN"`
	DBMaxConns       int32  `mapstructure:"DB_MAX_CONNS"`
	RabbitMQURL      string `mapstructure:"RABBITMQ_URL"`
	RetentionDays    int    `mapstructure:"AUDIT_RETENTION_DAYS"`
	TrustedGatewayIP string `mapstructure:"TRUSTED_GATEWAY_IP"`

	// ExportSecretsKeyHex (futures.md Tier 1 #4) is the 64-char hex
	// AES-256-GCM key used to seal hmac_secret + bearer_token on
	// audit_export_configs rows. Empty disables secret writes — Put
	// requests carrying a plaintext secret then return
	// FailedPrecondition with a clear error. Audit streaming over
	// syslog (which doesn't use HMAC) still works without the key.
	ExportSecretsKeyHex string `mapstructure:"AUDIT_EXPORT_SECRETS_KEY_HEX"`

	// RabbitMQMgmtURL (futures.md Tier 1 #4 Phase 2) overrides the
	// auto-derived RabbitMQ Management HTTP API endpoint used to
	// query live `audit.export.dlx` queue depth. Empty falls back to
	// `http://<rabbit-host>:15672` (RabbitMQ's default plugin port).
	// Set to your TLS-terminated mgmt endpoint in production.
	RabbitMQMgmtURL string `mapstructure:"RABBITMQ_MGMT_URL"`
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-audit")
	viper.SetDefault("DB_MAX_CONNS", 20)
	viper.SetDefault("AUDIT_RETENTION_DAYS", 365)
	viper.SetDefault("METRICS_ADDR", ":9090")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)

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
		"MTLS_CA_CERT_PATH": cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":    cfg.MTLSCertPath,
		"MTLS_KEY_PATH":     cfg.MTLSKeyPath,
		"DB_DSN":            cfg.DBDSN,
		"RABBITMQ_URL":      cfg.RabbitMQURL,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
