package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for the webhook service.
type Config struct {
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	LogFormat   string `mapstructure:"LOG_FORMAT"`
	GRPCAddr    string `mapstructure:"GRPC_ADDR"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	OTELExporter    string `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint    string `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName string `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment string `mapstructure:"OTEL_ENVIRONMENT"`

	DBDSN       string `mapstructure:"DB_DSN"`
	DBMaxConns  int32  `mapstructure:"DB_MAX_CONNS"`
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// CredentialKeyHex is the 32-byte AES-256-GCM key (hex-encoded) used to
	// encrypt HMAC secrets at rest. Required.
	CredentialKeyHex string `mapstructure:"CREDENTIAL_KEY_HEX"`

	// DeliveryPollIntervalSecs is how often the delivery worker polls for due retries.
	DeliveryPollIntervalSecs int `mapstructure:"DELIVERY_POLL_INTERVAL_SECS"`
	// DeliveryTimeoutSecs is the HTTP timeout per delivery attempt.
	DeliveryTimeoutSecs int `mapstructure:"DELIVERY_TIMEOUT_SECS"`
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-webhook")
	viper.SetDefault("DB_MAX_CONNS", 20)
	viper.SetDefault("DELIVERY_POLL_INTERVAL_SECS", 5)
	viper.SetDefault("DELIVERY_TIMEOUT_SECS", 30)

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
		"MTLS_CA_CERT_PATH":  cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":     cfg.MTLSCertPath,
		"MTLS_KEY_PATH":      cfg.MTLSKeyPath,
		"DB_DSN":             cfg.DBDSN,
		"RABBITMQ_URL":       cfg.RabbitMQURL,
		"CREDENTIAL_KEY_HEX": cfg.CredentialKeyHex,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
