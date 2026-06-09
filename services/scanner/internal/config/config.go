package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	LogLevel    string `mapstructure:"LOG_LEVEL"`
	LogFormat   string `mapstructure:"LOG_FORMAT"`
	GRPCAddr    string `mapstructure:"GRPC_ADDR"`
	HTTPAddr    string `mapstructure:"HTTP_ADDR"`

	MTLSCACertPath  string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath    string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath     string `mapstructure:"MTLS_KEY_PATH"`

	OTELExporter    string `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint    string `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName string `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment string `mapstructure:"OTEL_ENVIRONMENT"`
}

func Load() (*Config, error) {
	viper.AutomaticEnv()
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "json")
	viper.SetDefault("GRPC_ADDR", ":50051")
	viper.SetDefault("HTTP_ADDR", ":8080")
	viper.SetDefault("OTEL_SERVICE_NAME", "scanner")

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
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}
