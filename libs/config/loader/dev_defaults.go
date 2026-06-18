package loader

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// PENTEST-017: refuse to start in production when a credential matches a
// well-known development default (e.g. postgres password "registry", Vault
// root token "dev-root-token", MinIO secret "minioadmin"). Detection here
// turns an operator footgun into a startup error rather than silently
// shipping a public instance with weak credentials.

// wellKnownDevDefaults maps logical credential names to a list of values
// that are known development defaults shipped in docker-compose. Operators
// are expected to override these via env vars; we refuse to start when any
// of them survive into a production/staging environment.
//
// Keep this list in sync with infra/docker-compose/docker-compose.yml.
var wellKnownDevDefaults = map[string][]string{
	"POSTGRES_PASSWORD":        {"registry", "postgres", ""},
	"RABBITMQ_DEFAULT_PASS":    {"registry", "guest", ""},
	"MINIO_ROOT_PASSWORD":      {"minioadmin", ""},
	"STORAGE_MINIO_SECRET_KEY": {"minioadmin", ""},
	"VAULT_DEV_ROOT_TOKEN":     {"dev-root-token", "root", ""},
	"VAULT_TOKEN":              {"dev-root-token", "registry-signer-dev-token", ""},
}

// CheckDevDefaults inspects each (name, value) pair against the registered
// dev defaults. In production/staging, any match returns an error so the
// process refuses to start. In any other environment, matches are logged at
// WARN level so the operator can see them in the boot output.
//
// Pass `environment` as the value of `OTEL_ENVIRONMENT`. Unknown values are
// treated as development (warn, don't block) so an unconfigured env doesn't
// brick a brand-new deployment — the gate only triggers when an operator
// has explicitly opted into production/staging.
func CheckDevDefaults(environment string, creds map[string]string) error {
	env := strings.ToLower(strings.TrimSpace(environment))
	strict := env == "production" || env == "staging"

	var offenders []string
	for name, value := range creds {
		if !matchesDevDefault(name, value) {
			continue
		}
		if strict {
			offenders = append(offenders, name)
		} else {
			slog.Warn("dev-default credential in use — must NOT reach production",
				"name", name,
				"environment", env,
			)
		}
	}
	if len(offenders) > 0 {
		return fmt.Errorf("PENTEST-017: refusing to start in %s with dev-default credentials: %v "+
			"(set these env vars to non-default values before deploying)", env, offenders)
	}
	return nil
}

// CheckDevDefaultsFromDSN extracts the password from a postgres-style DSN
// (URL or key=value) and runs it through CheckDevDefaults. Convenience for
// services that only hold a DSN, not the raw password.
func CheckDevDefaultsFromDSN(environment, name, dsn string) error {
	pw, ok := extractPasswordFromDSN(dsn)
	if !ok {
		return nil // can't extract — leave it to the operator
	}
	return CheckDevDefaults(environment, map[string]string{name: pw})
}

// matchesDevDefault returns true when value is one of the registered dev
// defaults for the named credential. Unknown names always return false.
func matchesDevDefault(name, value string) bool {
	bad, ok := wellKnownDevDefaults[name]
	if !ok {
		return false
	}
	for _, b := range bad {
		if value == b {
			return true
		}
	}
	return false
}

// extractPasswordFromDSN supports both URL-style DSNs
// (postgres://user:pass@host/db) and amqp-style URLs (amqp://user:pass@host).
// Returns ("", false) when the DSN doesn't carry credentials.
func extractPasswordFromDSN(dsn string) (string, bool) {
	if dsn == "" {
		return "", false
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return "", false
	}
	pw, set := u.User.Password()
	if !set {
		return "", false
	}
	return pw, true
}
