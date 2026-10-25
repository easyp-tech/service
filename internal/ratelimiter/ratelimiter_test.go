package ratelimiter_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/ratelimiter"
)

// TestRateLimitRefusalSaysHowLongToWait pins the refusal text. It used to end
// in "retry after 1792932829", the unix time the bucket refills by.
func TestRateLimitRefusalSaysHowLongToWait(t *testing.T) {
	t.Parallel()

	rl := ratelimiter.New(ratelimiter.Config{
		RequestsPerSecond: 0.5,
		Burst:             1,
		CleanupInterval:   time.Minute,
	}, nil, keyFor("203.0.113.7"), slog.New(slog.DiscardHandler), nil, "easyp")

	ctx := context.Background()
	require.NoError(t, rl.Limit(ctx), "the first request fits the burst")

	err := rl.Limit(ctx)
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
	// One token at half a token per second is two seconds away.
	assert.Equal(t, "rate limit exceeded for 203.0.113.7, retry after 2s", st.Message())
}
