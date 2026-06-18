package config

import "testing"

// TestValidateAuthRealm — PENTEST-010: the validator must refuse an http://
// AUTH_REALM in production and staging, accept https:// everywhere, and only
// allow http:// in development (with a logged warning, not an error).
func TestValidateAuthRealm(t *testing.T) {
	cases := []struct {
		name        string
		realm       string
		environment string
		wantErr     bool
	}{
		{"https in production", "https://auth.example/auth/token", "production", false},
		{"https in development", "https://auth.example/auth/token", "development", false},
		{"http in production rejected", "http://localhost:8080/auth/token", "production", true},
		{"http in staging rejected", "http://localhost:8080/auth/token", "staging", true},
		{"http in development warned", "http://localhost:8080/auth/token", "development", false},
		{"http in unset environment warned", "http://localhost:8080/auth/token", "", false},
		{"empty realm tolerated", "", "production", false},
		{"ftp scheme rejected", "ftp://auth.example", "production", true},
		{"malformed url rejected", "://bad", "production", true},
		{"https case-insensitive scheme", "HTTPS://auth.example/token", "production", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateAuthRealm(c.realm, c.environment)
			if (err != nil) != c.wantErr {
				t.Errorf("validateAuthRealm(%q, %q) err = %v, wantErr = %v", c.realm, c.environment, err, c.wantErr)
			}
		})
	}
}
