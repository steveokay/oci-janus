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

	// JWT RS256 signing keys — base64-encoded PEM, never stored in plaintext.
	//
	// Phase 6.5 (multi-key rotation prep): these three env vars remain the
	// single-key path used by every existing deployment. To opt into the
	// multi-key ring set JWTKeyRingPath below; when it is set, these three
	// env vars become optional (a single-key ring is built from the
	// directory instead).
	JWTPrivateKeyB64 string `mapstructure:"JWT_PRIVATE_KEY_B64"`
	JWTPublicKeyB64  string `mapstructure:"JWT_PUBLIC_KEY_B64"`
	// JWTKeyID is the kid header value used for key rotation via JWKS.
	// Required only on the single-key path.
	JWTKeyID string `mapstructure:"JWT_KEY_ID"`

	// REDESIGN-001 Phase 6.5 — multi-key JWT ring for online rotation.
	//
	// JWTKeyRingPath is the optional path to a directory of PEM-encoded RSA
	// private keys. Each file becomes one ring entry; the file's base name
	// (without extension) is the kid. When set, the ring REPLACES the
	// single-key trio above: signing uses the kid named in JWTSigningKID
	// (or the lexicographically-greatest kid if JWTSigningKID is empty),
	// validation accepts any kid in the ring, and the JWKS endpoint
	// enumerates every public key.
	//
	// Per CLAUDE.md §7: if JWTKeyRingPath is set but unreadable, contains
	// no PEM files, or contains a malformed PEM, the service fails to
	// start. Silently falling back to single-key mode would defeat the
	// rotation surface.
	JWTKeyRingPath string `mapstructure:"JWT_KEY_RING_PATH"`
	// JWTSigningKID nominates which kid in the ring signs new tokens.
	// Optional — empty defaults to the lexicographically-greatest kid in
	// the ring, so operators using a monotonic naming convention
	// (timestamps, ULIDs) get auto-promotion on restart.
	JWTSigningKID string `mapstructure:"JWT_SIGNING_KID"`

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

	// REDESIGN-001 Phase 5.6 — SAML email-trust flag.
	//
	// SSOSAMLTrustEmail tells the SAML ACS handler whether to treat the
	// IdP-asserted email as already verified. SAML 2.0 does not carry a
	// standard `email_verified` claim; for many deployments (legacy ADFS,
	// custom IdPs, IdPs where the email attribute is operator-supplied at
	// federation time) the assertion email is NOT verified by the IdP, and
	// trusting it would expose an email-takeover vector — an attacker who
	// can get an unverified IdP account with victim@example.com would be
	// auto-provisioned as the legitimate victim.
	//
	// Default is false (fail-safe). Operators must opt in after confirming
	// their IdP actually verifies email addresses before issuing assertions
	// (Okta, Azure AD, Google Workspace, Auth0 typically do; raw ADFS does
	// not without explicit attribute-source configuration).
	//
	// When false, the SAML callback refuses login with a clear error and
	// the operator is expected either to set this flag to true or to wait
	// for the post-login email-verification flow (follow-up task).
	//
	// The Go zero value is false, which matches the documented default —
	// no viper.SetDefault call is required because libs/config/loader.Load
	// produces the zero value for any unset env var.
	SSOSAMLTrustEmail bool `mapstructure:"SSO_SAML_TRUST_EMAIL"`

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

	// REDESIGN-001 Phase 3.4 — tenant gRPC client (Phase 3.4 pilot service).
	//
	// TenantGRPCAddr is the host:port of registry-tenant's gRPC server.
	// Required in DEPLOYMENT_MODE=single so the auth service can fetch the
	// bootstrap_tenant_id from deployment_metadata at startup and wire
	// libs/middleware/grpc.SingleTenantInjector into its server chain.
	//
	// In DEPLOYMENT_MODE=multi this field is ignored — the injector is a
	// no-op when bootstrap_tenant_id is empty, so we skip the startup RPC
	// entirely to avoid an unnecessary dial.
	//
	// Reuses the same mTLS material as AuditGRPCAddr.
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`

	// DeploymentMode is the binary's posture, normalised by
	// libs/config/loader.LoadDeploymentMode. Empty env defaults to single.
	// Read in Load() — not via Viper bindings — to keep the validated/typed
	// value isolated from raw env string handling.
	DeploymentMode loader.DeploymentMode `mapstructure:"-"`

	// TrustedProxyCIDRs is a comma-separated CIDR list of trusted reverse
	// proxies (SEC-009). When the TCP peer falls within one of these
	// ranges, the leftmost non-private IP in X-Forwarded-For is used as
	// the client IP for rate limiting. Empty (default) ⇒ XFF is ignored
	// and RemoteAddr is always used. Malformed entries are logged and
	// skipped at startup.
	//
	// QA-006: env reads moved here from an init() in the handler package.
	TrustedProxyCIDRs string `mapstructure:"TRUSTED_PROXY_CIDRS"`

	// OIDCAllowedIssuers is the CSV of trusted OIDC issuer URL prefixes
	// for FUT-001 federated workload identity. The same allowlist gates
	// BOTH trust-create (at admin time) AND token-exchange (at runtime),
	// so removing an issuer here stops minting on the next exchange even
	// without a DB change.
	//
	// Empty/unset rejects ALL trust creation and ALL exchange requests
	// (fail-closed default for self-hosters who haven't named their CI
	// runners' IdPs yet). No production validation — empty is a valid
	// "feature off" state.
	OIDCAllowedIssuers string `mapstructure:"OIDC_ALLOWED_ISSUERS"`
}

// Load binds environment variables into Config and validates required fields.
// Fails fast at startup rather than surfacing missing secrets at request time.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := loader.Load("registry-auth", cfg); err != nil {
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
	// mTLS cert paths are required in production but optional for local dev.
	// The server will warn and run without TLS if they are absent.
	required := map[string]string{
		"DB_DSN":     cfg.DBDSN,
		"REDIS_ADDR": cfg.RedisAddr,
	}
	// Phase 6.5 — JWT key material can come from either the single-key
	// trio (JWT_PRIVATE_KEY_B64 + JWT_PUBLIC_KEY_B64 + JWT_KEY_ID) OR from
	// JWT_KEY_RING_PATH. Exactly one must be present; mixing them is
	// rejected so the operator cannot end up with two competing key sources
	// and a subtle bug about which one wins.
	if cfg.JWTKeyRingPath == "" {
		required["JWT_PRIVATE_KEY_B64"] = cfg.JWTPrivateKeyB64
		required["JWT_PUBLIC_KEY_B64"] = cfg.JWTPublicKeyB64
		required["JWT_KEY_ID"] = cfg.JWTKeyID
	} else if cfg.JWTPrivateKeyB64 != "" || cfg.JWTPublicKeyB64 != "" || cfg.JWTKeyID != "" {
		// Multi-key path: forbid the single-key envs so there is exactly
		// one source of truth for the active signing material.
		return fmt.Errorf("invalid config: JWT_KEY_RING_PATH is set; JWT_PRIVATE_KEY_B64 / JWT_PUBLIC_KEY_B64 / JWT_KEY_ID must be empty (pick exactly one path)")
	}
	return loader.RequireFields(required)
}
