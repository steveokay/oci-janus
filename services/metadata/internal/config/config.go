package config

import (
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
	// Required in DEPLOYMENT_MODE=single so metadata can fetch the
	// bootstrap_tenant_id at startup and wire libs/middleware/grpc.
	// SingleTenantInjector. In multi mode the value is ignored and the
	// dial is skipped (the injector is a no-op for empty bootstrap id).
	//
	// Cert paths reuse the per-server mTLS material in BaseConfig
	// (MTLS_CA_CERT_PATH / MTLS_CERT_PATH / MTLS_KEY_PATH).
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`

	// DeploymentMode is the binary's posture, normalised by
	// libs/config/loader.LoadDeploymentMode. Empty env defaults to single.
	// Read in Load() — not via Viper bindings — to keep the validated/typed
	// value isolated from raw env string handling.
	DeploymentMode loader.DeploymentMode `mapstructure:"-"`
}

// Load binds environment variables into Config and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-metadata", cfg); err != nil {
		return nil, err
	}
	// REDESIGN-001 Phase 3.4 — read DEPLOYMENT_MODE via the typed helper so
	// invalid values fail at startup. Defaults to single per the OSS posture.
	mode, err := loader.LoadDeploymentMode()
	if err != nil {
		return nil, fmt.Errorf("load deployment mode: %w", err)
	}
	cfg.DeploymentMode = mode
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	return loader.RequireFields(map[string]string{
		"DB_DSN":     cfg.DBDSN,
		"REDIS_ADDR": cfg.RedisAddr,
	})
}
