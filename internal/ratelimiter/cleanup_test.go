package ratelimiter_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/ratelimiter"
)

// TestStartCleanupSurvivesNonPositiveInterval is the second rubber band around
// the one setting in the whole config that could crash the process rather than
// degrade it.
//
// StartCleanup passes the interval to time.NewTicker, which panics on a
// non-positive value — from a background goroutine, outside the barrier that
// guards a cleanup pass, so it takes the process down *after* startup has
// already reported success. Config.Validate refuses such a value now, but a
// limiter built by hand (as several tests do) must not be able to panic either.
//
// The default is substituted in the constructor rather than in StartCleanup on
// purpose: the same field is the staleness threshold in cleanup, and if the two
// disagreed the sweep would delete every bucket on its first pass.
func TestStartCleanupSurvivesNonPositiveInterval(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		interval time.Duration
	}{
		{name: "zero", interval: 0},
		{name: "negative", interval: -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			limiter := ratelimiter.New(
				ratelimiter.Config{
					RequestsPerSecond: 10,
					Burst:             20,
					CleanupInterval:   tc.interval,
				},
				nil,
				keyFor("10.0.0.1"),
				slog.New(slog.DiscardHandler),
				nil,
				"easyp",
			)

			require.NotPanics(t, func() { limiter.StartCleanup(t.Context()) })

			// The limiter still works: a bucket exists and is not swept away by a
			// staleness threshold that collapsed to zero along with the interval.
			require.NoError(t, limiter.Limit(t.Context()))
			assert.NoError(t, limiter.Limit(t.Context()))
		})
	}
}

// TestDefaultConfigIsUsable pins the fallback the constructor reaches for.
// DefaultConfig had no caller outside tests, which is how the zero interval
// stayed reachable in the first place.
func TestDefaultConfigIsUsable(t *testing.T) {
	t.Parallel()

	cfg := ratelimiter.DefaultConfig()
	assert.Positive(t, cfg.CleanupInterval)
	assert.Positive(t, cfg.RequestsPerSecond)
	assert.Positive(t, cfg.Burst)
}
