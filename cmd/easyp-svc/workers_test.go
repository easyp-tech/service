package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/core"
)

// TestCappedWorkers pins the direction of the cut. The licence limits what a
// tier may use; it does not decide what a deployment wants. Assigning it
// outright — which is what this replaced — made the community ceiling of four
// into a floor, so `workers: 2` ran four and the configuration said otherwise.
func TestCappedWorkers(t *testing.T) {
	t.Parallel()

	discard := slog.New(slog.DiscardHandler)

	cases := []struct {
		name       string
		configured int
		limit      int
		want       int
	}{
		{
			name:       "a configuration under the limit is left alone",
			configured: 2,
			limit:      4,
			want:       2,
		},
		{
			name:       "a configuration over the limit is cut down to it",
			configured: 16,
			limit:      4,
			want:       4,
		},
		{
			name:       "asking for exactly the limit is not a cut",
			configured: 4,
			limit:      4,
			want:       4,
		},
		{
			name:       "an unlimited licence imposes nothing",
			configured: 64,
			limit:      core.LicenseUnlimited,
			want:       64,
		},
		{
			name:       "a licence that reports zero imposes nothing either",
			configured: 8,
			limit:      0,
			want:       8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, cappedByLicence("worker_pool.workers", tc.configured, tc.limit, discard))
		})
	}
}

// TestCommunityCeilingsMatchTheShippedDefaults is the check that keeps the
// generations ceiling from quietly becoming a take.
//
// The lever exists so the tier can be drawn on throughput later; the planks are
// set where the configuration already stands, so nobody who never touched the
// setting loses anything on the day it lands. If a default moves, this fails and
// the ceiling has to be moved with it — deliberately, rather than by drift.
func TestCommunityCeilingsMatchTheShippedDefaults(t *testing.T) {
	t.Parallel()

	defaults, err := config.Defaults(t.Context())
	require.NoError(t, err)

	claims := core.CommunityLicenseClaims()

	require.Equal(t, defaults.WorkerPool.Workers, claims.MaxWorkers,
		"the community worker ceiling must be the shipped default")
	require.Equal(t, defaults.WorkerPool.MaxConcurrentGenerations, claims.MaxGenerations,
		"the community generation ceiling must be the shipped default")
}

// TestEnterpriseHasNoGenerationCeiling pins the other half: the paid tier
// configures its own throughput.
func TestEnterpriseHasNoGenerationCeiling(t *testing.T) {
	t.Parallel()

	claims := core.EnterpriseLicenseClaims(time.Now().Add(time.Hour), false)

	require.Equal(t, core.LicenseUnlimited, claims.MaxGenerations)
	require.Equal(t, 64, cappedByLicence("worker_pool.max_concurrent_generations", 64,
		claims.MaxGenerations, slog.New(slog.DiscardHandler)))
}
