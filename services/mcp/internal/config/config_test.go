package config

import (
	"strings"
	"testing"
)

// validKey is a shape-conformant dummy API key. The regex enforces the
// FUT-006 bearer form; the underlying UUID + hex are otherwise arbitrary.
const (
	validKey      = "key.11111111-1111-1111-1111-111111111111.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validTenantID = "11111111-1111-1111-1111-111111111111"
)

// baseCfg returns a Config with every required field populated. Tests
// mutate one field to exercise a single failure branch at a time.
func baseCfg() *Config {
	return &Config{
		LogLevel:      "info",
		LogFormat:     "json",
		Transport:     TransportStdio,
		ManagementURL: "http://registry-management:8085",
		APIKey:        validKey,
		TenantID:      validTenantID,
	}
}

func TestValidate_OK_Stdio(t *testing.T) {
	// Golden path — stdio transport with all required fields present.
	if err := validate(baseCfg()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_OK_HTTP(t *testing.T) {
	// HTTP transport requires MCP_HTTP_ADDR — validate the accept path.
	cfg := baseCfg()
	cfg.Transport = TransportHTTP
	cfg.HTTPAddr = ":8087"
	if err := validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_HTTP_MissingAddr(t *testing.T) {
	cfg := baseCfg()
	cfg.Transport = TransportHTTP
	cfg.HTTPAddr = ""
	err := validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing MCP_HTTP_ADDR")
	}
	if !strings.Contains(err.Error(), "MCP_HTTP_ADDR") {
		t.Errorf("error should mention MCP_HTTP_ADDR: %v", err)
	}
}

func TestValidate_MissingManagementURL(t *testing.T) {
	cfg := baseCfg()
	cfg.ManagementURL = ""
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for empty ManagementURL")
	}
}

func TestValidate_MalformedManagementURL(t *testing.T) {
	// Rejects non-http scheme so operators don't accidentally point at
	// a gRPC target with the "grpc://" prefix they might invent.
	cfg := baseCfg()
	cfg.ManagementURL = "grpc://registry-management:50051"
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for non-http scheme")
	}
}

func TestValidate_MissingAPIKey(t *testing.T) {
	cfg := baseCfg()
	cfg.APIKey = ""
	err := validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty APIKey")
	}
	// Load-bearing: the error must NEVER leak the key itself.
	if strings.Contains(err.Error(), validKey) {
		t.Errorf("error must not leak API key: %v", err)
	}
}

func TestValidate_MalformedAPIKey(t *testing.T) {
	cfg := baseCfg()
	// Wrong prefix — must be "key.".
	cfg.APIKey = "bearer.11111111-1111-1111-1111-111111111111.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := validate(cfg)
	if err == nil {
		t.Fatal("expected error for malformed key prefix")
	}
	// SECURITY: error must include shape guidance, but NEVER the value.
	if strings.Contains(err.Error(), cfg.APIKey) {
		t.Errorf("error must not leak API key value: %v", err)
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error should say what's wrong: %v", err)
	}
}

func TestValidate_MalformedAPIKey_ShortSecret(t *testing.T) {
	// The regex requires 64 hex chars in the secret segment; anything
	// shorter must fail (protects against operator paste truncation).
	cfg := baseCfg()
	cfg.APIKey = "key.11111111-1111-1111-1111-111111111111.aaaa"
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for short secret segment")
	}
}

func TestValidate_MissingTenantID(t *testing.T) {
	cfg := baseCfg()
	cfg.TenantID = ""
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for empty TenantID")
	}
}

func TestValidate_MalformedTenantID(t *testing.T) {
	cfg := baseCfg()
	cfg.TenantID = "not-a-uuid"
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for non-UUID TenantID")
	}
}

func TestValidate_UnknownTransport(t *testing.T) {
	cfg := baseCfg()
	cfg.Transport = "sse" // valid MCP transport, but not one we support here
	if err := validate(cfg); err == nil {
		t.Fatal("expected error for unknown transport")
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	// Simulate a container with only the required env vars set — the
	// stdio transport + default log settings should fill themselves in.
	t.Setenv("MCP_MANAGEMENT_URL", "http://registry-management:8085")
	t.Setenv("MCP_API_KEY", validKey)
	t.Setenv("MCP_TENANT_ID", validTenantID)
	// Deliberately unset — Load's SetDefault must supply the value.
	// (Empty string via t.Setenv would still be picked up by viper's
	// automaticEnv scan and defeat the default.)
	t.Setenv("MCP_TRANSPORT", TransportStdio)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("Transport = %q, want %q", cfg.Transport, TransportStdio)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "json")
	}
}
