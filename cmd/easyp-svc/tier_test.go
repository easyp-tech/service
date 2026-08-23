package main

import (
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestServiceTierMismatchFollowsTheLicence pins the gauge to the live licence
// rather than the one resolved at boot. The scenario the gauge exists for — a
// licence expiring under a running deployment — happens long after startup, so
// a value computed once could never report it.
func TestServiceTierMismatchFollowsTheLicence(t *testing.T) {
	t.Parallel()

	tier := "enterprise"
	reg := prometheus.NewRegistry()

	checkServiceTier("enterprise", func() string { return tier }, slog.New(slog.DiscardHandler), reg, "test")

	require.Equal(t, 0, tierMismatchValue(t, reg),
		"a label that matches the licence is not a mismatch")

	tier = "community"
	require.Equal(t, 1, tierMismatchValue(t, reg),
		"the licence degraded under the running deployment; the gauge must notice without a restart")

	tier = "enterprise"
	require.Equal(t, 0, tierMismatchValue(t, reg),
		"a mismatch fixed by a licence refresh must clear the gauge")
}

func TestServiceTierEmptyLabelAssertsNothing(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	checkServiceTier("", func() string { return "community" }, slog.New(slog.DiscardHandler), reg, "test")

	require.Equal(t, 0, tierMismatchValue(t, reg),
		"a deployment that does not use the tier dimension cannot disagree with the licence")
}

// tierMismatchValue reads the gauge as an int: it only ever holds 0 or 1, so
// an exact integer comparison is the meaningful one.
func tierMismatchValue(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range families {
		if mf.GetName() == "test_config_service_tier_mismatch" {
			require.Len(t, mf.GetMetric(), 1)

			return int(mf.GetMetric()[0].GetGauge().GetValue())
		}
	}

	t.Fatal("config_service_tier_mismatch was not registered")

	return 0
}
