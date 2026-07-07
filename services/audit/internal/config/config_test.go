package config

import (
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
