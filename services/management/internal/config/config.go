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
	// TenantGRPCAddr is optional — only required when the super-admin tenant
	// CRUD API (`/api/v1/admin/tenants`) is enabled. Empty disables those
	// routes (they return 404 "route disabled") in the same pattern as
	// PLATFORM_ADMIN_TENANT_ID.
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`
	// WebhookGRPCAddr is optional — only required when the `/api/v1/webhooks`
	// routes are enabled. Empty disables those routes (they return 404
	// "route disabled").
	WebhookGRPCAddr string `mapstructure:"WEBHOOK_GRPC_ADDR"`

	// SignerGRPCAddr is optional — only required when FE-API-003
	// `/api/v1/.../signature` is enabled. Empty leaves that route at 404
	// "route disabled" so a deployment without registry-signer still
	// serves every other surface.
	SignerGRPCAddr string `mapstructure:"SIGNER_GRPC_ADDR"`

	// ScannerGRPCAddr is optional — required only when the FE-API-018
	// scan policies + FE-API-019 compliance report routes are enabled.
	// Empty leaves `/api/v1/security/policies` and
	// `/api/v1/security/reports/*` returning 404 "route disabled" so a
	// deployment without registry-scanner still serves every other surface.
	ScannerGRPCAddr string `mapstructure:"SCANNER_GRPC_ADDR"`

	// GCGRPCAddr is optional — required only when the FE-API-032 GC
	// status routes are enabled. Empty leaves
	// `/api/v1/admin/gc/{status,runs,run}` returning 404 "route
	// disabled" so a management deployment without registry-gc
	// running in the new persisted mode still serves every other surface.
	GCGRPCAddr string `mapstructure:"GC_GRPC_ADDR"`

	// ProxyGRPCAddr is optional — required only when the FUT-013
	// `/api/v1/proxy/cache` routes are enabled. Empty leaves those
	// routes returning 404 "route disabled" so deployments without
	// the pull-through proxy continue to serve every other surface.
	// The frontend probes the route and hides the sidebar entry when
	// the 404 lands, so an unset addr degrades gracefully.
	ProxyGRPCAddr string `mapstructure:"PROXY_GRPC_ADDR"`

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

	// DeploymentMode controls whether the FE renders multi-tenant chrome
	// (tenant switcher, plan badges) or single-tenant simplified UI.
	// Loaded via loader.LoadDeploymentMode() after Viper unmarshal.
	// Defaults to "single" (OSS self-hosted).
	DeploymentMode loader.DeploymentMode

	// BuildVersion is the binary version string injected at build time
	// (via -ldflags in main.go). Served by the /api/v1/deployment-info endpoint.
	// Not loaded from env — set by main() before calling server.Run().
	BuildVersion string
}

// Load binds environment variables into Config and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-management", cfg); err != nil {
		return nil, err
	}

	// Load deployment mode separately after Viper unmarshal.
	deploymentMode, err := loader.LoadDeploymentMode()
	if err != nil {
		return nil, fmt.Errorf("load deployment mode: %w", err)
	}
	cfg.DeploymentMode = deploymentMode

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

	// In production, CORS origin is mandatory.
	// mTLS cert validation is handled centrally in main.go via loader.ValidateMTLSConfig
	// (REDESIGN-001 Phase 1.3) — the ad-hoc per-service check here was removed.
	if cfg.OTELEnvironment == "production" {
		if cfg.CORSAllowedOrigin == "" {
			return fmt.Errorf("CORS_ALLOWED_ORIGIN is required in production")
		}
	}
	return nil
}
