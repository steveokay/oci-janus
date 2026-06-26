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
