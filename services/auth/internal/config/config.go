// Package config loads and validates runtime configuration for registry-auth.
// All values come from environment variables; no config files are read.
package config

import (
	"fmt"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config is the complete set of environment variables required by registry-auth.
// Embedded structs use mapstructure's squash tag so their fields map directly
// from env vars without any prefix.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`
	loader.DBConfig   `mapstructure:",squash"`

	// Redis — used for JTI revocation (REM-002) and per-IP login rate limiting
	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`

	// JWT RS256 signing keys — base64-encoded PEM, never stored in plaintext
	JWTPrivateKeyB64 string `mapstructure:"JWT_PRIVATE_KEY_B64"`
	JWTPublicKeyB64  string `mapstructure:"JWT_PUBLIC_KEY_B64"`
	// JWTKeyID is the kid header value used for key rotation via JWKS
	JWTKeyID string `mapstructure:"JWT_KEY_ID"`

	// DevDefaultTenantID is used in local dev when no X-Tenant-ID header is present.
	// Must not be set in production.
	DevDefaultTenantID string `mapstructure:"DEV_DEFAULT_TENANT_ID"`

	// RabbitMQURL is the AMQP connection URL for publishing RBAC audit events.
	// Required so that GrantRole / RevokeRole changes are traceable via registry-audit.
	RabbitMQURL string `mapstructure:"RABBITMQ_URL"`

	// FE-API-034 — SSO config.
	//
	// SSOCredentialKeyHex is the 64-character (32-byte) hex AES-256 key used
	// to encrypt OAuth client_secret values at rest. When empty, SSO routes
	// are not registered and the dashboard's SSO buttons fall back to "coming
	// soon". Required in production.
	SSOCredentialKeyHex string `mapstructure:"SSO_CREDENTIAL_KEY_HEX"`

	// SSOBaseURL is the public origin used to compose OAuth redirect_uri
	// values (e.g. "https://registry.example.com"). Must match what the IdP
	// has registered for the application.
	SSOBaseURL string `mapstructure:"SSO_BASE_URL"`

	// SAMLSPCertPath is the filesystem path to the PEM-encoded X.509 cert
	// the SP presents when signing SAML AuthnRequests. Paired with
	// SAMLSPKeyPath. When either is empty, SAML support is disabled — the
	// /auth/saml/... routes return 501.
	SAMLSPCertPath string `mapstructure:"SAML_SP_CERT_PATH"`

	// SAMLSPKeyPath is the filesystem path to the PEM-encoded RSA private
	// key paired with SAMLSPCertPath. Permissions should be chmod 600
	// (CLAUDE.md §7 — Cert key file permissions).
	SAMLSPKeyPath string `mapstructure:"SAML_SP_KEY_PATH"`
}

// Load binds environment variables into Config and validates required fields.
// Fails fast at startup rather than surfacing missing secrets at request time.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-auth", cfg); err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	// mTLS cert paths are required in production but optional for local dev.
	// The server will warn and run without TLS if they are absent.
	return loader.RequireFields(map[string]string{
		"DB_DSN":              cfg.DBDSN,
		"REDIS_ADDR":          cfg.RedisAddr,
		"JWT_PRIVATE_KEY_B64": cfg.JWTPrivateKeyB64,
		"JWT_PUBLIC_KEY_B64":  cfg.JWTPublicKeyB64,
		"JWT_KEY_ID":          cfg.JWTKeyID,
	})
}
