package license

import (
	"context"
	"log/slog"

	"github.com/easyp-tech/service/internal/core"
)

// MockLicenseClient stands in for the licence server that does not exist yet.
//
// It honours the wiring but not the cryptography: a deployment with no token,
// or one built without an embedded public key, runs in community mode, while
// any non-empty token is taken at face value.
//
// TODO: replace with a real client that verifies the PASETO signature against
// the configured public key and reads tier, limits and expiry from the claims.
type MockLicenseClient struct {
	token     string
	publicKey string
	logger    *slog.Logger
}

// NewMockLicenseClient creates a MockLicenseClient for the given token and
// verification key. An empty token means no licence was configured; an empty
// publicKey means there is nothing to verify one against. Both come from the
// license section of the configuration.
func NewMockLicenseClient(token, publicKey string, logger *slog.Logger) *MockLicenseClient {
	return &MockLicenseClient{
		token:     token,
		publicKey: publicKey,
		logger:    logger,
	}
}

// ValidateLicense reports the claims implied by the configured token.
func (m *MockLicenseClient) ValidateLicense(_ context.Context) (core.LicenseClaims, error) {
	if m.token == "" {
		return core.CommunityLicenseClaims(), nil
	}

	if m.publicKey == "" {
		m.logger.Warn(
			"licence token supplied but no public key is configured to verify it against; " +
				"running in community mode. Set license.public_key or LICENSE_PUBLIC_KEY.")

		return core.CommunityLicenseClaims(), nil
	}

	// Everything below is the part that is still missing: the token is trusted
	// on sight. Until the signature is actually checked, possession of any
	// non-empty string grants Enterprise.
	m.logger.Warn("licence token accepted WITHOUT signature verification: PASETO validation is not implemented yet")

	return core.LicenseClaims{
		Tier: core.LicenseTierEnterprise,
		Features: []core.Feature{
			core.FeatureCodeGeneration,
			core.FeaturePluginListing,
			core.FeatureMCPServerTools,
			core.FeatureRateLimiting,
			core.FeaturePluginCRUD,
			core.FeatureMultiTenancy,
			core.FeatureResponseCaching,
			core.FeatureAudit,
		},
		MaxWorkers: -1,
		MaxPlugins: -1,
	}, nil
}
