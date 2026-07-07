package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

// Config holds all runtime configuration for the audit service.
//
// RED-FU-014 — the standard BaseConfig fields (LogLevel/LogFormat/
// GRPCAddr/HTTPAddr/MetricsAddr/MTLS_*/OTEL_*) are inherited via the
// squashed loader.BaseConfig embed so they live in one canonical place
// and gain the cfg.MTLSClientCreds(serverName) method from RED-FU-012.
type Config struct {
	loader.BaseConfig `mapstructure:",squash"`

	DBDSN            string `mapstructure:"DB_DSN"`
	DBMaxConns       int32  `mapstructure:"DB_MAX_CONNS"`
	RabbitMQURL      string `mapstructure:"RABBITMQ_URL"`
	RetentionDays    int    `mapstructure:"AUDIT_RETENTION_DAYS"`
	TrustedGatewayIP string `mapstructure:"TRUSTED_GATEWAY_IP"`

	// ExportSecretsKeyHex (futures.md Tier 1 #4) is the 64-char hex
	// AES-256-GCM key used to seal hmac_secret + bearer_token on
	// audit_export_configs rows. Empty disables secret writes — Put
	// requests carrying a plaintext secret then return
	// FailedPrecondition with a clear error. Audit streaming over
	// syslog (which doesn't use HMAC) still works without the key.
	ExportSecretsKeyHex string `mapstructure:"AUDIT_EXPORT_SECRETS_KEY_HEX"`

	// NotifyEmailKeyHex (FUT-019 Phase 3) is the 64-char hex AES-256-GCM key
	// sealing email_transport_config secrets (resend_api_key / smtp_password).
	// Empty disables the email channel: transport RPCs writing a secret return
	// FailedPrecondition and the send loop idles. Set-but-not-32-bytes fails
	// closed at startup (a bad KEK would silently corrupt secrets).
	NotifyEmailKeyHex string `mapstructure:"NOTIFY_EMAIL_KEY_HEX"`

	// AuthGRPCAddr (FUT-019 Phase 3) is the mTLS target for
	// registry-auth.ResolveUserEmails, used by the dispatcher to resolve
	// recipient email addresses. Empty disables email fan-out.
	AuthGRPCAddr string `mapstructure:"AUTH_GRPC_ADDR"`

	// PlatformHost (FUT-019 Phase 3) is the public base URL (scheme + host,
	// no trailing path) used by the email send loop to build absolute CTA
	// links, e.g. "https://registry.example.com". Optional + unvalidated:
	// empty leaves email links relative, which still resolve in-app.
	PlatformHost string `mapstructure:"PLATFORM_HOST"`

	// RabbitMQMgmtURL (futures.md Tier 1 #4 Phase 2) overrides the
	// auto-derived RabbitMQ Management HTTP API endpoint used to
	// query live `audit.export.dlx` queue depth. Empty falls back to
	// `http://<rabbit-host>:15672` (RabbitMQ's default plugin port).
	// Set to your TLS-terminated mgmt endpoint in production.
	RabbitMQMgmtURL string `mapstructure:"RABBITMQ_MGMT_URL"`

	// REDESIGN-001 Phase 3.4 — tenant gRPC client for SingleTenantInjector.
	// In single mode the audit gRPC server pins every inbound RPC to the
	// bootstrap tenant fetched from registry-tenant's GetDeploymentMetadata
	// at startup. Required when DEPLOYMENT_MODE=single.
	TenantGRPCAddr string `mapstructure:"TENANT_GRPC_ADDR"`

	// DeploymentMode is the binary's posture, normalised by
	// libs/config/loader.LoadDeploymentMode. Empty env defaults to single.
	DeploymentMode loader.DeploymentMode `mapstructure:"-"`
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
	viper.SetDefault("OTEL_SERVICE_NAME", "registry-audit")
	viper.SetDefault("DB_MAX_CONNS", 20)
	viper.SetDefault("AUDIT_RETENTION_DAYS", 365)
	viper.SetDefault("METRICS_ADDR", ":9090")
	viper.SetDefault("OTEL_SAMPLING_RATE", 1.0)

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	// REDESIGN-001 Phase 3.4 — read DEPLOYMENT_MODE via the typed helper.
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
	required := map[string]string{
		"MTLS_CA_CERT_PATH": cfg.MTLSCACertPath,
		"MTLS_CERT_PATH":    cfg.MTLSCertPath,
		"MTLS_KEY_PATH":     cfg.MTLSKeyPath,
		"DB_DSN":            cfg.DBDSN,
		"RABBITMQ_URL":      cfg.RabbitMQURL,
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	// FUT-019 Phase 3 — email KEK is optional (unset disables email), but a
	// set-but-malformed key must fail closed rather than silently corrupt rows.
	if cfg.NotifyEmailKeyHex != "" {
		if _, err := hex.DecodeString(cfg.NotifyEmailKeyHex); err != nil {
			return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: not valid hex: %w", err)
		}
		if len(cfg.NotifyEmailKeyHex) != 64 {
			return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: expected 64 hex chars (32 bytes), got %d", len(cfg.NotifyEmailKeyHex))
		}
	}
	return nil
}
