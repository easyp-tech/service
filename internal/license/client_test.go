package license

import (
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// testPublicKey stands in for the key the linker normally supplies.
const testPublicKey = "c4c720019f4c70dcb30f3cdbac7f73689c6e027d02cb8ae8c5a3cbe654cfb6e0"

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestMockLicenseClientValidate(t *testing.T) {
	t.Parallel()

	t.Run("no token means community", func(t *testing.T) {
		t.Parallel()

		claims, err := NewMockLicenseClient("", testPublicKey, discardLogger()).ValidateLicense(t.Context())
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierCommunity, claims.Tier)
		require.NotContains(t, claims.Features, core.FeatureAudit)
	})

	t.Run("a token without a configured public key means community", func(t *testing.T) {
		t.Parallel()

		claims, err := NewMockLicenseClient("some-token", "", discardLogger()).ValidateLicense(t.Context())
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierCommunity, claims.Tier)
		require.NotContains(t, claims.Features, core.FeatureAudit)
	})

	t.Run("token plus public key grants enterprise", func(t *testing.T) {
		t.Parallel()

		claims, err := NewMockLicenseClient("some-token", testPublicKey, discardLogger()).ValidateLicense(t.Context())
		require.NoError(t, err)
		require.Equal(t, core.LicenseTierEnterprise, claims.Tier)
		require.Contains(t, claims.Features, core.FeatureAudit)
		require.Equal(t, -1, claims.MaxWorkers)
	})
}

// TestCommunityGateDeniesAudit pins the property the whole gate exists for:
// without a licence, audit is off and the community limits apply.
func TestCommunityGateDeniesAudit(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(
		t.Context(),
		NewMockLicenseClient("", "", discardLogger()),
		Config{}, //nolint:exhaustruct // CacheTTL falls back to its default
		discardLogger(),
		prometheus.NewRegistry(),
		"test",
	)
	require.NoError(t, err)

	gate := NewFeatureGate(manager)
	require.False(t, gate.Enabled(core.FeatureAudit))
	require.True(t, gate.Enabled(core.FeatureCodeGeneration))
	require.Equal(t, 4, gate.MaxWorkers())
	require.Equal(t, 10, gate.MaxPlugins())
}

// TestEnterpriseGateAllowsAudit is the other half: with a licence, audit is on.
func TestEnterpriseGateAllowsAudit(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(
		t.Context(),
		NewMockLicenseClient("some-token", testPublicKey, discardLogger()),
		Config{}, //nolint:exhaustruct // CacheTTL falls back to its default
		discardLogger(),
		prometheus.NewRegistry(),
		"test",
	)
	require.NoError(t, err)

	require.True(t, NewFeatureGate(manager).Enabled(core.FeatureAudit))
}
