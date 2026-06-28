package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config holds all runtime configuration for the signer service.
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

	// SIGNER_KEY_BACKEND selects the key source: env | vault | awskms | gcpkms | azurekms
	SignerKeyBackend string `mapstructure:"SIGNER_KEY_BACKEND"`

	// env backend: PEM-encoded ECDSA P-256 private/public key, base64-encoded.
	// Never log these values.
	CosignPrivateKeyB64 string `mapstructure:"SIGNER_COSIGN_PRIVATE_KEY"`
	CosignPublicKeyB64  string `mapstructure:"SIGNER_COSIGN_PUBLIC_KEY"`

	// vault backend
	VaultAddr       string `mapstructure:"VAULT_ADDR"`
	VaultToken      string `mapstructure:"VAULT_TOKEN"`
	VaultCosignPath string `mapstructure:"VAULT_COSIGN_PATH"`

	// KMS backends (awskms / gcpkms / azurekms)
	KMSKeyARN     string `mapstructure:"SIGNER_KMS_ARN"`
	KMSResourceID string `mapstructure:"SIGNER_KMS_RESOURCE_ID"`
	KMSVaultURL   string `mapstructure:"SIGNER_KMS_VAULT_URL"`
	KMSKeyName    string `mapstructure:"SIGNER_KMS_KEY_NAME"`

	// Database — required in production for durable signature persistence (SEC-015).
	// When empty the service falls back to an in-memory store with a startup warning;
	// this is acceptable for local development but MUST NOT be used in production.
	// Connection string format: postgres://user:pass@host:5432/registry_signer?sslmode=require
	DBDSN string `mapstructure:"SIGNER_DB_DSN"`

	// RabbitMQURL is the broker the FUT-017 cache.populated consumer connects to.
	// Empty disables inbound event consumption entirely (the signer still exposes
	// the synchronous SignManifest RPC). Format: amqp://user:pass@host:5672/
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-signer")
	viper.SetDefault("SIGNER_KEY_BACKEND", "env")
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
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}

	validBackends := map[string]bool{
		"env": true, "vault": true, "awskms": true, "gcpkms": true, "azurekms": true,
	}
	if !validBackends[cfg.SignerKeyBackend] {
		return fmt.Errorf("SIGNER_KEY_BACKEND must be one of: env, vault, awskms, gcpkms, azurekms")
	}

	if cfg.SignerKeyBackend == "env" {
		if cfg.CosignPrivateKeyB64 == "" {
			return fmt.Errorf("SIGNER_COSIGN_PRIVATE_KEY is required for env backend")
		}
		if cfg.CosignPublicKeyB64 == "" {
			return fmt.Errorf("SIGNER_COSIGN_PUBLIC_KEY is required for env backend")
		}
	}
	if cfg.SignerKeyBackend == "vault" {
		if cfg.VaultAddr == "" {
			return fmt.Errorf("VAULT_ADDR is required for vault backend")
		}
		if cfg.VaultToken == "" {
			return fmt.Errorf("VAULT_TOKEN is required for vault backend")
		}
		if cfg.VaultCosignPath == "" {
			return fmt.Errorf("VAULT_COSIGN_PATH is required for vault backend (e.g. transit/sign/registry-signer)")
		}
		// PENTEST-017: reject the dev-mode Vault token in production.
		if err := loader.CheckDevDefaults(cfg.OTELEnvironment, map[string]string{
			"VAULT_TOKEN": cfg.VaultToken,
		}); err != nil {
			return err
		}
	}
	return nil
}
