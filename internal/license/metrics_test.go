package license

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// gaugeValue reads a gauge back out of the registry, which is what Prometheus
// would scrape. Asserting on the struct field would pass even if the metric were
// never registered.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

		metrics := family.GetMetric()
		require.Len(t, metrics, 1, "%s should have exactly one series", name)

		return metrics[0].GetGauge().GetValue()
	}

	t.Fatalf("metric %q was never registered", name)

	return 0
}

// TestMetricsObserve pins what an operator can actually alert on.
//
// All three gauges were wrong before this: expiry was registered and never set,
// so it read 0 forever, and valid was set from the absence of an error — but
// ValidateLicense reports community mode as success, so it read 1 on an
// installation with no licence at all. Alerting on either was impossible.
func TestMetricsObserve(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2027, 7, 31, 0, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		claims      core.LicenseClaims
		wantValid   float64
		wantExpiry  float64
		wantInGrace float64
	}{
		"enterprise in force": {
			claims:      core.EnterpriseLicenseClaims(expiry, false),
			wantValid:   1,
			wantExpiry:  float64(expiry.Unix()),
			wantInGrace: 0,
		},
		"enterprise on its grace period": {
			claims:      core.EnterpriseLicenseClaims(expiry, true),
			wantValid:   1,
			wantExpiry:  float64(expiry.Unix()),
			wantInGrace: 1,
		},
		// The case that used to report valid=1. A community installation is not a
		// failure, but it is not a licence either.
		"community": {
			claims:      core.CommunityLicenseClaims(),
			wantValid:   0,
			wantExpiry:  0,
			wantInGrace: 0,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewRegistry()
			NewMetrics(reg, "easyp").observe(test.claims)

			// Gauges are float64 but every value here is an exact integer, so a
			// zero tolerance is the honest comparison.
			require.InDelta(t, test.wantValid, gaugeValue(t, reg, "easyp_license_valid"), 0)
			require.InDelta(t, test.wantExpiry, gaugeValue(t, reg, "easyp_license_expiry_timestamp_seconds"), 0)
			require.InDelta(t, test.wantInGrace, gaugeValue(t, reg, "easyp_license_in_grace"), 0)
		})
	}
}

// TestManagerPublishesMetrics covers the wiring: the gauges have to move when
// the manager refreshes, not just when observe is called directly.
func TestManagerPublishesMetrics(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(30 * 24 * time.Hour)
	reg := prometheus.NewRegistry()
	client := &stubClient{claims: core.EnterpriseLicenseClaims(expiry, false)}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		discardLogger(), reg, "easyp")
	require.NoError(t, err)

	require.InDelta(t, float64(expiry.Unix()), gaugeValue(t, reg, "easyp_license_expiry_timestamp_seconds"), 1)
	require.InDelta(t, 1, gaugeValue(t, reg, "easyp_license_valid"), 0)

	// Losing the licence must show up as a drop to 0, not as a stale 1.
	client.claims = core.CommunityLicenseClaims()
	manager.refresh(t.Context())

	require.InDelta(t, 0, gaugeValue(t, reg, "easyp_license_valid"), 0)
	require.InDelta(t, 0, gaugeValue(t, reg, "easyp_license_expiry_timestamp_seconds"), 0)
}

// TestMetricsUnchangedOnValidationError guards the deliberate omission in
// refresh: a failed check leaves the previous claims in force, so the gauges
// must keep describing them rather than a state nothing is enforcing.
func TestMetricsUnchangedOnValidationError(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(30 * 24 * time.Hour)
	reg := prometheus.NewRegistry()
	client := &stubClient{claims: core.EnterpriseLicenseClaims(expiry, false)}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		discardLogger(), reg, "easyp")
	require.NoError(t, err)

	client.err = ErrNoClient
	manager.refresh(t.Context())

	require.InDelta(t, 1, gaugeValue(t, reg, "easyp_license_valid"), 0,
		"a failed refresh must not report community while Enterprise claims are still enforced")
}
