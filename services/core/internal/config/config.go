package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for registry-core.
type Config struct {
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	LogFormat   string `mapstructure:"LOG_FORMAT"`
	GRPCAddr    string `mapstructure:"GRPC_ADDR"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	AuthGRPCAddr     string `mapstructure:"AUTH_GRPC_ADDR"`
	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`

	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	OTELExporter    string `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint    string `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName string `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment string `mapstructure:"OTEL_ENVIRONMENT"`
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	viper.AutomaticEnv()
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "json")
	viper.SetDefault("GRPC_ADDR", ":50052")
	viper.SetDefault("HTTP_ADDR", ":8081")
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-core")
	viper.SetDefault("REDIS_ADDR", "localhost:6379")
	viper.SetDefault("AUTH_GRPC_ADDR", "registry-auth:50051")
	viper.SetDefault("METADATA_GRPC_ADDR", "registry-metadata:50051")
	viper.SetDefault("STORAGE_GRPC_ADDR", "registry-storage:50051")

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
	return nil
}
