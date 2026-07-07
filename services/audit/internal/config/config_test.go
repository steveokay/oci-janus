package config

import (
	"strings"
	"testing"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

func TestValidate_notifyEmailKey_wrongLengthFailsClosed(t *testing.T) {
	cfg := &Config{
		BaseConfig: loader.BaseConfig{
			MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k",
		},
		DBDSN: "postgres://x", RabbitMQURL: "amqp://x",
		NotifyEmailKeyHex: "abcd", // not 64 hex chars
	}
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a set-but-short NOTIFY_EMAIL_KEY_HEX")
	}
}

func TestValidate_notifyEmailKey_unsetIsAllowed(t *testing.T) {
	cfg := &Config{
		BaseConfig: loader.BaseConfig{
			MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k",
		},
		DBDSN: "postgres://x", RabbitMQURL: "amqp://x",
		NotifyEmailKeyHex: "", // unset → email disabled, not an error
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("unset NOTIFY_EMAIL_KEY_HEX must be allowed, got %v", err)
	}
}

// TestValidate_webhookKEK exercises the FUT-019 webhook-channel KEK, which
// mirrors the email KEK's fail-closed posture: unset disables the channel,
// a set-but-malformed key is rejected at startup, and a valid 64-hex key
// passes.
func TestValidate_webhookKEK(t *testing.T) {
	// base returns a minimal-valid config with all required fields set, so
	// only NotifyWebhookKeyHex is under test.
	base := func() *Config {
		return &Config{
			BaseConfig: loader.BaseConfig{
				MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k",
			},
			DBDSN: "postgres://x", RabbitMQURL: "amqp://x",
		}
	}

	// (a) unset → webhook channel disabled, not an error.
	cfg := base()
	cfg.NotifyWebhookKeyHex = ""
	if err := validate(cfg); err != nil {
		t.Fatalf("unset NOTIFY_WEBHOOK_KEY_HEX must be allowed, got %v", err)
	}

	// (b) short (not 64 hex chars) → fail closed.
	cfg = base()
	cfg.NotifyWebhookKeyHex = "abcd"
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a set-but-short NOTIFY_WEBHOOK_KEY_HEX")
	}

	// (c) valid 64-hex (32 bytes) → allowed.
	cfg = base()
	cfg.NotifyWebhookKeyHex = strings.Repeat("ab", 32)
	if err := validate(cfg); err != nil {
		t.Fatalf("valid 64-hex NOTIFY_WEBHOOK_KEY_HEX must be allowed, got %v", err)
	}
}
