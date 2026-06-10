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

	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	OTELExporter    string `mapstructure:"OTEL_EXPORTER"`
	OTELEndpoint    string `mapstructure:"OTEL_ENDPOINT"`
	OTELServiceName string `mapstructure:"OTEL_SERVICE_NAME"`
	OTELEnvironment string `mapstructure:"OTEL_ENVIRONMENT"`

	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	StorageGRPCAddr  string `mapstructure:"STORAGE_GRPC_ADDR"`
	RabbitMQURL      string `mapstructure:"RABBITMQ_URL"`

	// GCMode controls what the collector deletes: dry-run | manifests | blobs | full
	GCMode               string `mapstructure:"GC_MODE"`
	GCRunIntervalHours   int    `mapstructure:"GC_RUN_INTERVAL_HOURS"`
	BlobMinAgeHours      int    `mapstructure:"GC_BLOB_MIN_AGE_HOURS"`
	ManifestMinAgeHours  int    `mapstructure:"GC_MANIFEST_MIN_AGE_HOURS"`
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-gc")
	viper.SetDefault("GC_MODE", "full")
	viper.SetDefault("GC_RUN_INTERVAL_HOURS", 24)
	viper.SetDefault("GC_BLOB_MIN_AGE_HOURS", 1)
	viper.SetDefault("GC_MANIFEST_MIN_AGE_HOURS", 24)

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
