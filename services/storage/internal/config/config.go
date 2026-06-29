// Package config loads and validates runtime configuration for registry-storage.
package config

import (
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config is the complete set of environment variables required by registry-storage.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`

	// Storage driver: minio | s3 | gcs | azure | filesystem
	StorageDriver string `mapstructure:"STORAGE_DRIVER"`

	// MinIO / S3-compatible
	StorageMinIOEndpoint  string `mapstructure:"STORAGE_MINIO_ENDPOINT"`
	StorageMinIOAccessKey string `mapstructure:"STORAGE_MINIO_ACCESS_KEY"`
	StorageMinIOSecretKey string `mapstructure:"STORAGE_MINIO_SECRET_KEY"`
	StorageMinIOBucket    string `mapstructure:"STORAGE_MINIO_BUCKET"`
	StorageMinIOUseSSL    bool   `mapstructure:"STORAGE_MINIO_USE_SSL"`
	StorageMinIORegion    string `mapstructure:"STORAGE_MINIO_REGION"`

	// Filesystem (dev only)
	StorageFilesystemRoot string `mapstructure:"STORAGE_FILESYSTEM_ROOT"`

	// REDESIGN-001 Phase 3.4 — tenant gRPC client for SingleTenantInjector.
	// See services/auth + services/core for the rollout pattern. In single
	// mode storage dials services/tenant at startup and wires
	// libs/middleware/grpc.SingleTenantInjector; in multi mode the dial is
	// skipped (the injector is a no-op for empty bootstrap id anyway).
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
	cfg.StorageMinIOUseSSL = true // default true
	if err := loader.Load("registry-storage", cfg); err != nil {
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
	if cfg.StorageDriver == "" {
		return fmt.Errorf("STORAGE_DRIVER is required (minio|s3|gcs|azure|filesystem)")
	}
	valid := map[string]bool{"minio": true, "s3": true, "gcs": true, "azure": true, "filesystem": true}
	if !valid[cfg.StorageDriver] {
		return fmt.Errorf("STORAGE_DRIVER %q is not valid; must be one of minio|s3|gcs|azure|filesystem", cfg.StorageDriver)
	}
	// PENTEST-017: refuse to ship the well-known dev MinIO secret to production.
	if err := loader.CheckDevDefaults(cfg.OTELEnvironment, map[string]string{
		"STORAGE_MINIO_SECRET_KEY": cfg.StorageMinIOSecretKey,
	}); err != nil {
		return err
	}
	return nil
}
