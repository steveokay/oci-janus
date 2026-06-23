package exportworker

// filter.go — shared event-filter + secret-open helpers used by the
// consumer. Duplicated from services/audit/internal/eventconsumer
// so this package stays a leaf in the dependency graph (no circular
// import). Both copies operate on the same audit_export_configs row
// shape so behaviour is identical — if one ever diverges from the
// other, that's a bug.

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
)

// matchesFilter applies the include/exclude allowlist on the
// audit-action string. Empty filter ⇒ "send everything."
func matchesFilter(raw json.RawMessage, action string) bool {
	if len(raw) == 0 {
		return true
	}
	var f struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return true
	}
	for _, p := range f.Exclude {
		if matchPattern(p, action) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, p := range f.Include {
		if matchPattern(p, action) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, s string) bool {
	if pattern == s {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// openSecret decrypts a sealed BYTEA column. Returns "" for nil/empty
// input so the renderer can branch on `secret == ""`.
func openSecret(key, ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if len(key) == 0 {
		return "", errors.New("audit-export secrets key not configured")
	}
	plain, err := aes.Decrypt(ciphertext, key)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
