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

	// REDESIGN-001 RM-003 — SSO config (global model).
	//
	// SSOCredentialKeyHex is the 64-character (32-byte) hex AES-256 key used
	// to decrypt OAuth client_secret_enc values stored in global_sso_config.
	// When empty, SSO routes are not registered. Required in production.
	SSOCredentialKeyHex string `mapstructure:"SSO_CREDENTIAL_KEY_HEX"`

	// DefaultTenantID is the fallback tenant UUID used when auto-provisioning
	// a new SSO user and no existing user row can be matched by email
	// (RM-004: tenant_id is no longer stored in the SSO session). Required
	// for single-tenant self-hosted deployments where all users belong to one
	// tenant. Multi-tenant routing is a follow-up item.
	DefaultTenantID string `mapstructure:"AUTH_DEFAULT_TENANT_ID"`

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

	// FE-API-048 FUT-005 — audit gRPC client.
	//
	// AuditGRPCAddr is the host:port of registry-audit's gRPC server. When
	// empty, the ActivityService is NOT constructed and /api/v1/access/activity
	// returns 501 NOT_IMPLEMENTED. Required so per-principal activity feeds
	// resolve in production.
	//
	// The connection reuses the gRPC mTLS material in BaseConfig
	// (MTLS_CA_CERT_PATH / MTLS_CLIENT_CERT_PATH / MTLS_CLIENT_KEY_PATH).
	// In production all three cert paths must be set; in local dev the
	// dial falls back to plaintext with a slog.Warn.
	AuditGRPCAddr string `mapstructure:"AUDIT_GRPC_ADDR"`

	// TrustedProxyCIDRs is a comma-separated CIDR list of trusted reverse
	// proxies (SEC-009). When the TCP peer falls within one of these
	// ranges, the leftmost non-private IP in X-Forwarded-For is used as
	// the client IP for rate limiting. Empty (default) ⇒ XFF is ignored
	// and RemoteAddr is always used. Malformed entries are logged and
	// skipped at startup.
	//
	// QA-006: env reads moved here from an init() in the handler package.
	TrustedProxyCIDRs string `mapstructure:"TRUSTED_PROXY_CIDRS"`
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
