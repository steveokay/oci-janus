package config

import (
	"strings"
	"testing"
)

// TestValidate_prRegistryKEK exercises the FUT-023 Phase 1 PR-registry KEK,
// which mirrors the audit notification KEKs' fail-closed posture: unset
// disables the feature, a set-but-malformed key is rejected at startup, and a
// valid 64-hex key passes.
func TestValidate_prRegistryKEK(t *testing.T) {
	// base returns a minimal-valid config with all required fields set so only
	// PRRegistryKeyHex is under test. REDIS_ADDR + DB_DSN are the fields
	// validate() requires via loader.RequireFields.
	base := func() *Config {
		cfg := &Config{}
		cfg.DBDSN = "postgres://x"
		cfg.RedisAddr = "localhost:6379"
		return cfg
	}

	// (a) unset → PR-registry feature disabled, not an error.
	cfg := base()
	cfg.PRRegistryKeyHex = ""
	if err := validate(cfg); err != nil {
		t.Fatalf("unset PR_REGISTRY_KEY_HEX must be allowed, got %v", err)
	}

	// (b) valid 64-hex (32 bytes) → allowed.
	cfg = base()
	cfg.PRRegistryKeyHex = strings.Repeat("ab", 32)
	if err := validate(cfg); err != nil {
		t.Fatalf("valid 64-hex PR_REGISTRY_KEY_HEX must be allowed, got %v", err)
	}

	// (c) short (32 hex chars = 16 bytes, not 32) → fail closed. Still valid
	// hex, so this exercises the length gate, not the hex-decode gate.
	cfg = base()
	cfg.PRRegistryKeyHex = strings.Repeat("ab", 16)
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a set-but-32-char PR_REGISTRY_KEY_HEX")
	}

	// (d) 63 chars (one short of 64) → fail closed on the length gate.
	cfg = base()
	cfg.PRRegistryKeyHex = strings.Repeat("a", 63)
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a 63-char PR_REGISTRY_KEY_HEX")
	}

	// (e) 64 chars but not valid hex → fail closed on the hex-decode gate.
	cfg = base()
	cfg.PRRegistryKeyHex = strings.Repeat("zz", 32)
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a non-hex PR_REGISTRY_KEY_HEX")
	}
}
