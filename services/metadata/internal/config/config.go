package config

import (
	"encoding/hex"
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config is the complete set of environment variables required by registry-metadata.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`
	loader.DBConfig   `mapstructure:",squash"`

	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`

	// RabbitMQURL (FE-API-042) — required for the pull.image consumer that
	// drives manifests.last_pulled_at updates. Optional in the sense that an
	// empty string disables the consumer (handy for unit tests + offline
	// dev), but the service logs a WARN on startup when it isn't set.
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// REDESIGN-001 Phase 3.4 — tenant gRPC client for SingleTenantInjector.
	//
	// TenantGRPCAddr is the host:port of registry-tenant's gRPC server.
	// Required so metadata can fetch the
	// bootstrap_tenant_id at startup and wire libs/middleware/grpc.
	// SingleTenantInjector. In multi mode the value is ignored and the
	// dial is skipped (the injector is a no-op for empty bootstrap id).
	//
	// Cert paths reuse the per-server mTLS material in BaseConfig
	// (MTLS_CA_CERT_PATH / MTLS_CERT_PATH / MTLS_KEY_PATH).
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`

	// PRRegistryKeyHex (FUT-023 Phase 1) is the 64-char hex AES-256-GCM KEK
	// that seals the per-tenant GitHub webhook secret used by ephemeral
	// PR-scoped registries. Empty disables the PR-registry feature entirely
	// (the webhook receiver, wired in a later task, refuses to seal/verify).
	// Set-but-not-32-bytes fails closed at startup (a bad KEK would silently
	// corrupt the sealed secret), mirroring the audit notification KEKs.
	// The raw hex stays on the config; the hex->[]byte decode + handler
	// wiring happens in internal/server (a separate FUT-023 task).
	PRRegistryKeyHex string `mapstructure:"PR_REGISTRY_KEY_HEX"`
}

// Load binds environment variables into Config and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-metadata", cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if err := loader.RequireFields(map[string]string{
		"DB_DSN":     cfg.DBDSN,
		"REDIS_ADDR": cfg.RedisAddr,
	}); err != nil {
		return err
	}
	// FUT-023 Phase 1 — the PR-registry KEK is optional (unset disables the
	// feature), but a set-but-malformed key must fail closed rather than
	// silently corrupt the sealed GitHub webhook secret. Mirrors the audit
	// NOTIFY_EMAIL_KEY_HEX validation.
	if cfg.PRRegistryKeyHex != "" {
		if _, err := hex.DecodeString(cfg.PRRegistryKeyHex); err != nil {
			return fmt.Errorf("PR_REGISTRY_KEY_HEX: not valid hex: %w", err)
		}
		if len(cfg.PRRegistryKeyHex) != 64 {
			return fmt.Errorf("PR_REGISTRY_KEY_HEX: expected 64 hex chars (32 bytes), got %d", len(cfg.PRRegistryKeyHex))
		}
	}
	return nil
}
