package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for the scanner service.
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

	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`

	PluginPath     string `mapstructure:"SCANNER_PLUGIN_PATH"`
	PluginChecksum string `mapstructure:"SCANNER_PLUGIN_CHECKSUM"`

	WorkerCount    int `mapstructure:"SCANNER_WORKER_COUNT"`
	JobTimeoutSecs int `mapstructure:"SCANNER_JOB_TIMEOUT_SECS"`
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	viper.AutomaticEnv()
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "json")
	viper.SetDefault("GRPC_ADDR", ":50051")
	viper.SetDefault("HTTP_ADDR", ":8080")
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-scanner")
	viper.SetDefault("SCANNER_WORKER_COUNT", 4)
	viper.SetDefault("SCANNER_JOB_TIMEOUT_SECS", 600)

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
		"MTLS_CA_CERT_PATH":    cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":       cfg.MTLSCertPath,
		"MTLS_KEY_PATH":        cfg.MTLSKeyPath,
		"RABBITMQ_URL":         cfg.RabbitMQURL,
		"METADATA_GRPC_ADDR":   cfg.MetadataGRPCAddr,
		"STORAGE_GRPC_ADDR":    cfg.StorageGRPCAddr,
		"SCANNER_PLUGIN_PATH":  cfg.PluginPath,
		"SCANNER_PLUGIN_CHECKSUM": cfg.PluginChecksum,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
