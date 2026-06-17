// Package config_test contains unit tests for the gateway config validation logic.
// No environment variables are relied upon — all values are set directly.
package config

import (
	"testing"
)

// ── validate() tests ──────────────────────────────────────────────────────────

func TestValidate_AllRequired_ReturnsNil(t *testing.T) {
	cfg := &Config{
		MTLSCACertPath: "/certs/ca.pem",
		MTLSCertPath:   "/certs/cert.pem",
		MTLSKeyPath:    "/certs/key.pem",
	}
	if err := validate(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_MissingCACertPath_ReturnsError(t *testing.T) {
	cfg := &Config{
		MTLSCACertPath: "",
		MTLSCertPath:   "/certs/cert.pem",
		MTLSKeyPath:    "/certs/key.pem",
	}
	if err := validate(cfg); err == nil {
		t.Error("expected error when MTLS_CA_CERT_PATH is empty")
	}
}

func TestValidate_MissingCertPath_ReturnsError(t *testing.T) {
	cfg := &Config{
		MTLSCACertPath: "/certs/ca.pem",
		MTLSCertPath:   "",
		MTLSKeyPath:    "/certs/key.pem",
	}
	if err := validate(cfg); err == nil {
		t.Error("expected error when MTLS_CERT_PATH is empty")
	}
}

func TestValidate_MissingKeyPath_ReturnsError(t *testing.T) {
	cfg := &Config{
		MTLSCACertPath: "/certs/ca.pem",
		MTLSCertPath:   "/certs/cert.pem",
		MTLSKeyPath:    "",
	}
	if err := validate(cfg); err == nil {
		t.Error("expected error when MTLS_KEY_PATH is empty")
	}
}

func TestValidate_AllEmpty_ReturnsError(t *testing.T) {
	cfg := &Config{}
	if err := validate(cfg); err == nil {
		t.Error("expected error when all mTLS fields are empty")
	}
}
