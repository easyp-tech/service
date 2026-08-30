package sdk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestIsTransient pins which codes the client retries. This is a contract, not
// an implementation detail: it decides what a server under load gets back from
// a fleet of SDK callers, and freezing the SDK freezes it.
func TestIsTransient(t *testing.T) {
	t.Parallel()

	retried := map[codes.Code]bool{
		codes.Unavailable: true,

		// Refusals the service means. ResourceExhausted carries both
		// ErrServerOverloaded — retrying which answers "I have no capacity"
		// with more load — and ErrMaxPluginsExceeded, a licence ceiling that
		// no amount of waiting clears.
		codes.ResourceExhausted: false,

		// A generation that hit the server's timeout already spent
		// worker_pool.generation_timeout running a plugin. Retrying spends it
		// again for the same answer.
		codes.DeadlineExceeded: false,

		codes.InvalidArgument:    false,
		codes.NotFound:           false,
		codes.AlreadyExists:      false,
		codes.PermissionDenied:   false,
		codes.Unauthenticated:    false,
		codes.Internal:           false,
		codes.FailedPrecondition: false,
		codes.Canceled:           false,
	}

	for code, want := range retried {
		assert.Equalf(t, want, isTransient(status.Error(code, "x")),
			"code %s", code)
	}

	// A plain error is not a status and must not be retried.
	assert.False(t, isTransient(errors.New("boom")))
}

// TestBackoffDelayDoesNotPanic covers the overflow that WithMaxRetries could
// reach. The option is public and unbounded; at the default 100ms base,
// baseDelay<<40 is already negative, and rand.Int64N panics on a non-positive
// bound — so a large retry budget plus one Unavailable crashed the caller from
// inside the interceptor.
func TestBackoffDelayDoesNotPanic(t *testing.T) {
	t.Parallel()

	base, maxDelay := 100*time.Millisecond, 5*time.Second

	for _, attempt := range []int{0, 1, 5, 30, 40, 62, 63, 1000} {
		got := backoffDelay(base, maxDelay, attempt)

		assert.Positivef(t, got, "attempt %d produced a non-positive delay", attempt)
		assert.LessOrEqualf(t, got, maxDelay, "attempt %d exceeded maxDelay", attempt)
	}
}

// TestBackoffDelayGrows checks the shape is still exponential in the range a
// default client actually uses, jitter included.
func TestBackoffDelayGrows(t *testing.T) {
	t.Parallel()

	base, maxDelay := 100*time.Millisecond, 5*time.Second

	// attempt 0 -> [100ms, 125ms], attempt 2 -> [400ms, 500ms].
	assert.GreaterOrEqual(t, backoffDelay(base, maxDelay, 0), base)
	assert.LessOrEqual(t, backoffDelay(base, maxDelay, 0), base+base/4)
	assert.GreaterOrEqual(t, backoffDelay(base, maxDelay, 2), 4*base)
}

// TestRetryInterceptorStopsOnPermanentError is the behaviour the code above
// buys: a refusal the server means costs exactly one round trip.
func TestRetryInterceptorStopsOnPermanentError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		code  codes.Code
		calls int
	}{
		{"overload is not retried", codes.ResourceExhausted, 1},
		{"timeout is not retried", codes.DeadlineExceeded, 1},
		{"unavailable is retried", codes.Unavailable, 4}, // 1 attempt + 3 retries
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0

			interceptor := retryUnaryInterceptor(3, time.Millisecond, 5*time.Millisecond)
			err := interceptor(context.Background(), "/svc/M", nil, nil, nil,
				func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					calls++

					return status.Error(tc.code, "x")
				})

			require.Error(t, err)
			assert.Equal(t, tc.calls, calls)
		})
	}
}
