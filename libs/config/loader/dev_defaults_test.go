package loader

import (
	"strings"
	"testing"
)

// TestCheckDevDefaults_blocksDefaultsInProduction — PENTEST-017: in production
// the loader must refuse to start when any credential matches a registered
// dev default.
func TestCheckDevDefaults_blocksDefaultsInProduction(t *testing.T) {
	err := CheckDevDefaults("production", map[string]string{
		"POSTGRES_PASSWORD":    "registry",
		"VAULT_DEV_ROOT_TOKEN": "dev-root-token",
	})
	if err == nil {
		t.Fatal("expected error in production with default creds, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") || !strings.Contains(err.Error(), "VAULT_DEV_ROOT_TOKEN") {
		t.Errorf("error should name both offenders, got: %v", err)
	}
}

// TestCheckDevDefaults_blocksDefaultsInStaging mirrors production behaviour.
func TestCheckDevDefaults_blocksDefaultsInStaging(t *testing.T) {
	err := CheckDevDefaults("staging", map[string]string{
		"MINIO_ROOT_PASSWORD": "minioadmin",
	})
	if err == nil {
		t.Fatal("expected error in staging with default creds, got nil")
	}
}

// TestCheckDevDefaults_allowsDefaultsInDevelopment verifies that dev usage
// (the whole point of dev creds existing) is not blocked.
func TestCheckDevDefaults_allowsDefaultsInDevelopment(t *testing.T) {
	err := CheckDevDefaults("development", map[string]string{
		"POSTGRES_PASSWORD": "registry",
		"VAULT_TOKEN":       "dev-root-token",
	})
	if err != nil {
		t.Errorf("dev environment should not block default creds, got: %v", err)
	}
}

// TestCheckDevDefaults_allowsStrongCredsInProduction confirms the happy path.
func TestCheckDevDefaults_allowsStrongCredsInProduction(t *testing.T) {
	err := CheckDevDefaults("production", map[string]string{
		"POSTGRES_PASSWORD":    "Tg!9rN@2pK*4xQwLs7vBz#mF",
		"VAULT_TOKEN":          "hvs.AbCdEfGh1234ZyXwVuTsRq",
		"STORAGE_MINIO_SECRET_KEY": "Y3qK!7nB^4xLpD@9rTfA*2vM",
	})
	if err != nil {
		t.Errorf("strong creds in prod should not error, got: %v", err)
	}
}

// TestCheckDevDefaults_unknownEnvironmentTreatedAsDev — if the operator
// hasn't set OTEL_ENVIRONMENT we should warn rather than block, because
// failing-closed on an unset env would brick fresh deployments.
func TestCheckDevDefaults_unknownEnvironmentTreatedAsDev(t *testing.T) {
	err := CheckDevDefaults("", map[string]string{
		"POSTGRES_PASSWORD": "registry",
	})
	if err != nil {
		t.Errorf("empty env should warn-not-block, got: %v", err)
	}
}

// TestCheckDevDefaults_unknownCredNameIgnored — names we don't track must
// not generate spurious errors.
func TestCheckDevDefaults_unknownCredNameIgnored(t *testing.T) {
	err := CheckDevDefaults("production", map[string]string{
		"SOME_CUSTOM_API_KEY": "registry", // same value as a known default but different name
	})
	if err != nil {
		t.Errorf("unknown credential name should not be checked, got: %v", err)
	}
}

// TestExtractPasswordFromDSN covers URL-style + key=value Postgres DSNs and
// amqp URLs. We only need the URL form to work since that's what the project
// uses in compose; key=value is just for completeness.
func TestExtractPasswordFromDSN(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		wantPw  string
		wantOK  bool
	}{
		{"postgres URL", "postgres://registry:registry@postgres:5432/db?sslmode=require", "registry", true},
		{"amqp URL", "amqp://registry:registry@rabbitmq:5672/", "registry", true},
		{"strong password", "postgres://u:Tg!9rN%402pK@host/db", "Tg!9rN@2pK", true},
		{"no password in URL", "postgres://registry@host/db", "", false},
		{"empty dsn", "", "", false},
		{"non-URL form unsupported", "user=registry password=registry host=postgres", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := extractPasswordFromDSN(c.dsn)
			if got != c.wantPw || ok != c.wantOK {
				t.Errorf("extractPasswordFromDSN(%q) = (%q, %v), want (%q, %v)", c.dsn, got, ok, c.wantPw, c.wantOK)
			}
		})
	}
}

// TestCheckDevDefaultsFromDSN end-to-end: extract password from a DSN and
// run the production check.
func TestCheckDevDefaultsFromDSN(t *testing.T) {
	err := CheckDevDefaultsFromDSN("production", "POSTGRES_PASSWORD",
		"postgres://registry:registry@postgres:5432/registry?sslmode=require")
	if err == nil {
		t.Fatal("expected dev-default password in DSN to block production startup")
	}

	err = CheckDevDefaultsFromDSN("production", "POSTGRES_PASSWORD",
		"postgres://registry:Tg%219rN%402pK@postgres:5432/registry?sslmode=require")
	if err != nil {
		t.Errorf("strong password in DSN should not block production startup, got: %v", err)
	}
}
