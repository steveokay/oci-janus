package loader

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
