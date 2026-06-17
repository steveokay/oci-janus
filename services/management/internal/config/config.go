// Package config loads and validates runtime configuration for registry-management.
// All values come from environment variables; no config files are read.
package config

import (
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config is the complete set of environment variables required by registry-management.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`

	// HTTP listen address for the management REST API.
	HTTPAddr string `mapstructure:"HTTP_ADDR"`

	// gRPC addresses of upstream services (host:port).
	AuthGRPCAddr     string `mapstructure:"AUTH_GRPC_ADDR"`
	MetadataGRPCAddr string `mapstructure:"METADATA_GRPC_ADDR"`
	AuditGRPCAddr    string `mapstructure:"AUDIT_GRPC_ADDR"`

	// RabbitMQ connection URL for publishing scan.queued events.
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// CORS_ALLOWED_ORIGIN is the single origin permitted to call the API from a browser.
	// Must be set explicitly — never wildcarded. Dev default: http://localhost:5173.
	CORSAllowedOrigin string `mapstructure:"CORS_ALLOWED_ORIGIN"`

	// mTLS — optional in dev, required in production.
	MTLSCACertPath string `mapstructure:"MTLS_CA_CERT_PATH"`
	MTLSCertPath   string `mapstructure:"MTLS_CERT_PATH"`
	MTLSKeyPath    string `mapstructure:"MTLS_KEY_PATH"`

	// PlatformAdminTenantID identifies the single tenant whose admin/owner users
	// can call cross-tenant platform operations (currently only the per-tenant
	// storage quota route). Empty disables those routes entirely. In local dev
	// this should be set to the seeded dev tenant id; in production it should be
	// a dedicated "operator" tenant created out of band.
	PlatformAdminTenantID string `mapstructure:"PLATFORM_ADMIN_TENANT_ID"`
}

// Load binds environment variables into Config and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-management", cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if err := loader.RequireFields(map[string]string{
		"AUTH_GRPC_ADDR":     cfg.AuthGRPCAddr,
		"METADATA_GRPC_ADDR": cfg.MetadataGRPCAddr,
		"RABBITMQ_URL":       cfg.RabbitMQURL,
		"AUDIT_GRPC_ADDR":    cfg.AuditGRPCAddr,
	}); err != nil {
		return err
	}

	// In production, CORS origin and mTLS certs are mandatory.
	// Plaintext gRPC in production would expose every token validation response.
	if cfg.OTELEnvironment == "production" {
		if cfg.CORSAllowedOrigin == "" {
			return fmt.Errorf("CORS_ALLOWED_ORIGIN is required in production")
		}
		if cfg.MTLSCACertPath == "" || cfg.MTLSCertPath == "" || cfg.MTLSKeyPath == "" {
			return fmt.Errorf("MTLS_CA_CERT_PATH, MTLS_CERT_PATH, and MTLS_KEY_PATH are all required in production")
		}
	}
	return nil
}
