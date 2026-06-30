// Package config_test validates the config loading and validation logic for
// registry-auth. Tests use t.Setenv so that environment variable side-effects
// are automatically rolled back after each test (no manual cleanup needed).
package config

import (
	"strings"
	"testing"
)

// minimalValidEnv returns the minimum set of environment variables required for
// a successful config.Load() call. Each test that needs a different value
// overrides the relevant key with t.Setenv after calling this helper.
func setMinimalValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_DSN", "postgres://user:pass@localhost:5432/auth?sslmode=require")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("JWT_PRIVATE_KEY_B64", "ZmFrZXByaXZhdGVrZXk=") // base64("fakeprivatekey")
	t.Setenv("JWT_PUBLIC_KEY_B64", "ZmFrZXB1YmxpY2tleQ==")  // base64("fakepublickey")
	t.Setenv("JWT_KEY_ID", "key-v1")
}

// TestLoad_validEnv verifies that a complete set of required environment
// variables allows Load() to succeed without error.
func TestLoad_validEnv_succeeds(t *testing.T) {
	setMinimalValidEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with valid env: unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}
}

// TestLoad_missingDBDSN verifies that omitting DB_DSN causes Load() to fail.
func TestLoad_missingDBDSN_returnsError(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("DB_DSN", "") // clear the required field

	_, err := Load()
	if err == nil {
		t.Error("expected error when DB_DSN is empty, got nil")
	}
}

// TestLoad_missingRedisAddr verifies that omitting REDIS_ADDR causes Load() to fail.
func TestLoad_missingRedisAddr_returnsError(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("REDIS_ADDR", "")

	_, err := Load()
	if err == nil {
		t.Error("expected error when REDIS_ADDR is empty, got nil")
	}
}

// TestLoad_missingJWTPrivateKey verifies that omitting JWT_PRIVATE_KEY_B64 causes an error.
func TestLoad_missingJWTPrivateKey_returnsError(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("JWT_PRIVATE_KEY_B64", "")

	_, err := Load()
	if err == nil {
		t.Error("expected error when JWT_PRIVATE_KEY_B64 is empty, got nil")
	}
}

// TestLoad_missingJWTPublicKey verifies that omitting JWT_PUBLIC_KEY_B64 causes an error.
func TestLoad_missingJWTPublicKey_returnsError(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("JWT_PUBLIC_KEY_B64", "")

	_, err := Load()
	if err == nil {
		t.Error("expected error when JWT_PUBLIC_KEY_B64 is empty, got nil")
	}
}

// TestLoad_missingJWTKeyID verifies that omitting JWT_KEY_ID causes an error.
func TestLoad_missingJWTKeyID_returnsError(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("JWT_KEY_ID", "")

	_, err := Load()
	if err == nil {
		t.Error("expected error when JWT_KEY_ID is empty, got nil")
	}
}

// TestLoad_configuredValuesPopulated verifies that env var values are correctly
// mapped into the returned Config struct fields.
func TestLoad_configuredValues_populatedCorrectly(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("REDIS_ADDR", "redis.internal:6379")
	t.Setenv("JWT_KEY_ID", "prod-key-2026")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.RedisAddr != "redis.internal:6379" {
		t.Errorf("RedisAddr: got %q, want %q", cfg.RedisAddr, "redis.internal:6379")
	}
	if cfg.JWTKeyID != "prod-key-2026" {
		t.Errorf("JWTKeyID: got %q, want %q", cfg.JWTKeyID, "prod-key-2026")
	}
}

// TestLoad_errorContainsMissingFieldName verifies that the error message
// produced when a required field is absent includes the env var name, making it
// actionable for operators.
func TestLoad_errorContainsMissingFieldName_helpsOperators(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("JWT_KEY_ID", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "JWT_KEY_ID") {
		t.Errorf("error does not mention JWT_KEY_ID: %v", err)
	}
}

// TestLoad_devDefaultTenantID verifies that the optional DEV_DEFAULT_TENANT_ID
// field is populated when set and has no effect when absent.
func TestLoad_devDefaultTenantID_optional(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("DEV_DEFAULT_TENANT_ID", "11111111-1111-1111-1111-111111111111")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DevDefaultTenantID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("DevDefaultTenantID: got %q, want the UUID we set", cfg.DevDefaultTenantID)
	}
}

// TestLoad_multipleMissingFields verifies that all missing fields are reported
// together rather than failing on the first missing one.
func TestLoad_multipleMissingFields_reportsAll(t *testing.T) {
	// Do NOT call setMinimalValidEnv — leave everything unset.
	t.Setenv("DB_DSN", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("JWT_PRIVATE_KEY_B64", "")
	t.Setenv("JWT_PUBLIC_KEY_B64", "")
	t.Setenv("JWT_KEY_ID", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The error should mention at least one of the missing keys.
	for _, key := range []string{"DB_DSN", "REDIS_ADDR", "JWT_PRIVATE_KEY_B64", "JWT_PUBLIC_KEY_B64", "JWT_KEY_ID"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %q: %v", key, err)
		}
	}
}

// TestLoad_RejectsMixedJWTConfig is the SEC-049 / qa-agent regression: when
// JWT_KEY_RING_PATH is set, all three legacy single-key vars
// (JWT_PRIVATE_KEY_B64, JWT_PUBLIC_KEY_B64, JWT_KEY_ID) MUST be empty.
// Mixing the two paths is a misconfiguration (which signing key is real?);
// Load() must reject it with a clear error rather than silently fall back to
// one path or the other.
func TestLoad_RejectsMixedJWTConfig(t *testing.T) {
	setMinimalValidEnv(t)
	// Operator set a ring path AND left the legacy vars populated by mistake.
	t.Setenv("JWT_KEY_RING_PATH", "/etc/registry/keys")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load() to reject mixed legacy + ring config, got nil")
	}
	if !strings.Contains(err.Error(), "JWT_KEY_RING_PATH") {
		t.Errorf("error must name JWT_KEY_RING_PATH so the operator knows which var to clear: %v", err)
	}
}
