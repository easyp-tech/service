package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

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

			require.Equal(t, tc.want, cappedWorkers(tc.configured, tc.limit, discard))
		})
	}
}
