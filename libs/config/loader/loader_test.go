package loader

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoadDeploymentMode verifies that DEPLOYMENT_MODE is parsed,
// defaults to "single", and rejects unknown values at startup.
func TestLoadDeploymentMode(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    DeploymentMode
		wantErr bool
	}{
		{"default is single", "", DeploymentModeSingle, false},
		{"explicit single", "single", DeploymentModeSingle, false},
		{"explicit multi", "multi", DeploymentModeMulti, false},
		{"unknown rejected", "saas", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DEPLOYMENT_MODE", tt.env)
			cfg, err := LoadDeploymentMode()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg)
		})
	}
}

// TestValidateMTLSConfig enforces that when MTLS_REQUIRED=true,
// empty cert paths fail loudly at startup. This replaces the
// per-service ad-hoc check (only management had it before).
func TestValidateMTLSConfig(t *testing.T) {
	t.Run("required + empty fails", func(t *testing.T) {
		err := ValidateMTLSConfig(MTLSConfig{Required: true})
		require.ErrorContains(t, err, "MTLS_CA_CERT_PATH")
	})
	t.Run("required + all set passes", func(t *testing.T) {
		err := ValidateMTLSConfig(MTLSConfig{
			Required: true, CACertPath: "/a", CertPath: "/b", KeyPath: "/c",
		})
		require.NoError(t, err)
	})
	t.Run("not required + empty passes (dev)", func(t *testing.T) {
		err := ValidateMTLSConfig(MTLSConfig{Required: false})
		require.NoError(t, err)
	})
}
