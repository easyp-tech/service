package ratelimiter_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/ratelimiter"
)

// keyFor returns an extractor reporting a fixed client, standing in for the
// peer address so the tests do not need a real connection.
func keyFor(client string) ratelimiter.KeyExtractor {
	return func(context.Context) string { return client }
}

func newLimiter(limit int, key ratelimiter.KeyExtractor) *ratelimiter.ConcurrencyLimiter {
	return ratelimiter.NewConcurrencyLimiter(limit, nil, key, slog.New(slog.DiscardHandler), nil, "easyp")
}

func streamInfo() *grpc.StreamServerInfo {
	return &grpc.StreamServerInfo{FullMethod: "/test/Method"} //nolint:exhaustruct // only FullMethod is read
}

func unaryInfo() *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: "/test/Method"} //nolint:exhaustruct // only FullMethod is read
}

// TestConcurrencyLimitRejectsExcess holds one request open and checks that the
// caller's next one is refused rather than queued.
func TestConcurrencyLimitRejectsExcess(t *testing.T) {
	t.Parallel()

	limiter := newLimiter(1, keyFor("10.0.0.1"))
	interceptor := limiter.UnaryServerInterceptor()

	held := make(chan struct{})
	entered := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, _ = interceptor(t.Context(), nil, unaryInfo(), func(context.Context, any) (any, error) {
			close(entered)
			<-held

			return nil, nil
		})
	})

	<-entered

	_, err := interceptor(t.Context(), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Error("handler ran despite the client being at its limit")

		return nil, nil
	})

	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))

	close(held)
	wg.Wait()
}

// TestConcurrencySlotIsReleased checks that finishing a request frees the slot,
// so a limit of one does not permanently lock a client out.
func TestConcurrencySlotIsReleased(t *testing.T) {
	t.Parallel()

	limiter := newLimiter(1, keyFor("10.0.0.2"))
	interceptor := limiter.UnaryServerInterceptor()

	ran := 0
	handler := func(context.Context, any) (any, error) {
		ran++

		return nil, nil
	}

	for range 3 {
		_, err := interceptor(t.Context(), nil, unaryInfo(), handler)
		require.NoError(t, err)
	}

	assert.Equal(t, 3, ran, "sequential requests must each get the slot back")
}

// TestConcurrencyIsPerClient pins the point of the limiter: one noisy caller
// must not consume the allowance of another.
func TestConcurrencyIsPerClient(t *testing.T) {
	t.Parallel()

	client := "10.0.0.3"
	limiter := ratelimiter.NewConcurrencyLimiter(
		1, nil,
		func(context.Context) string { return client },
		slog.New(slog.DiscardHandler), nil, "easyp",
	)
	interceptor := limiter.UnaryServerInterceptor()

	held := make(chan struct{})
	entered := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		_, _ = interceptor(t.Context(), nil, unaryInfo(), func(context.Context, any) (any, error) {
			close(entered)
			<-held

			return nil, nil
		})
	})

	<-entered

	// The first client is saturated; a different address must still get through.
	client = "10.0.0.4"

	served := false

	_, err := interceptor(t.Context(), nil, unaryInfo(), func(context.Context, any) (any, error) {
		served = true

		return nil, nil
	})

	require.NoError(t, err)
	assert.True(t, served, "a second client was refused because the first was busy")

	close(held)
	wg.Wait()
}

// TestConcurrencyDisabled checks that a zero limit lets everything through.
func TestConcurrencyDisabled(t *testing.T) {
	t.Parallel()

	limiter := newLimiter(0, keyFor("10.0.0.5"))
	interceptor := limiter.StreamServerInterceptor()

	var wg sync.WaitGroup

	held := make(chan struct{})
	started := make(chan struct{}, 5)

	for range 5 {
		wg.Go(func() {
			_ = interceptor(nil, fakeStream{ctx: t.Context()}, streamInfo(), func(any, grpc.ServerStream) error {
				started <- struct{}{}
				<-held

				return nil
			})
		})
	}

	for range 5 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("a stream was blocked despite the limiter being disabled")
		}
	}

	close(held)
	wg.Wait()
}

// fakeStream is a grpc.ServerStream carrying only a context.
type fakeStream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // a ServerStream exposes its context by design
}

func (f fakeStream) Context() context.Context { return f.ctx }
