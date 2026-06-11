package config

import (
	"encoding/hex"
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config holds all runtime configuration for registry-proxy.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`
	loader.DBConfig   `mapstructure:",squash"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`

	AuthGRPCAddr    string `mapstructure:"AUTH_GRPC_ADDR"`
	StorageGRPCAddr string `mapstructure:"STORAGE_GRPC_ADDR"`

	// RabbitMQURL is optional. When set, failed background blob stores are published
	// as store.queued events so a consumer can retry them durably.
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// CredentialKeyHex is a 64-character hex string (32 bytes) used for
	// AES-256-GCM encryption of upstream registry passwords at rest.
	CredentialKeyHex string `mapstructure:"CREDENTIAL_KEY_HEX"`

	// UpstreamHTTPTimeoutSecs controls per-request timeout to upstream registries.
	UpstreamHTTPTimeoutSecs int `mapstructure:"UPSTREAM_HTTP_TIMEOUT_SECS"`
	// UpstreamMaxResponseBytes caps the response body size per upstream layer (default 20 GiB).
	UpstreamMaxResponseBytes int64 `mapstructure:"UPSTREAM_MAX_RESPONSE_BYTES"`
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-proxy", cfg); err != nil {
		return nil, err
	}
	if err := loader.RequireFields(map[string]string{
		"REDIS_ADDR":         cfg.RedisAddr,
		"AUTH_GRPC_ADDR":     cfg.AuthGRPCAddr,
		"STORAGE_GRPC_ADDR":  cfg.StorageGRPCAddr,
		"DB_DSN":             cfg.DBDSN,
		"CREDENTIAL_KEY_HEX": cfg.CredentialKeyHex,
	}); err != nil {
		return nil, err
	}
	if len(cfg.CredentialKeyHex) != 64 {
		return nil, fmt.Errorf("CREDENTIAL_KEY_HEX must be 64 hex characters (32 bytes), got %d", len(cfg.CredentialKeyHex))
	}
	if _, err := hex.DecodeString(cfg.CredentialKeyHex); err != nil {
		return nil, fmt.Errorf("CREDENTIAL_KEY_HEX is not valid hex: %w", err)
	}
	if cfg.UpstreamHTTPTimeoutSecs == 0 {
		cfg.UpstreamHTTPTimeoutSecs = 30
	}
	if cfg.UpstreamMaxResponseBytes == 0 {
		cfg.UpstreamMaxResponseBytes = 20 << 30 // 20 GiB per CLAUDE.md §4.6
	}
	return cfg, nil
}
