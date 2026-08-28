package sdk

import (
	"context"
	"math/rand/v2"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// retryUnaryInterceptor returns a gRPC unary client interceptor that retries
// transient errors with exponential backoff and jitter.
//
// Transient codes: UNAVAILABLE, DEADLINE_EXCEEDED, RESOURCE_EXHAUSTED.
// Delay formula: min(baseDelay * 2^attempt + jitter, maxDelay), jitter up to 25%.
func retryUnaryInterceptor(maxRetries int, baseDelay, maxDelay time.Duration) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		var lastErr error
		for attempt := range maxRetries + 1 {
			lastErr = invoker(ctx, method, req, reply, cc, opts...)
			if lastErr == nil {
				return nil
			}

			if !isTransient(lastErr) {
				return lastErr
			}

			// Don't sleep after the last attempt.
			if attempt == maxRetries {
				break
			}

			delay := backoffDelay(baseDelay, maxDelay, attempt)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		return lastErr
	}
}

// isTransient reports whether err is a transient gRPC error that should be
// retried.
//
// Unavailable only. The other two codes this used to retry both made things
// worse:
//
// ResourceExhausted is what the service returns for ErrServerOverloaded — a
// server saying it has no capacity — and retrying it is a fleet of clients
// answering that with more load, three times each, exactly when the server can
// least afford it. The same code also carries ErrMaxPluginsExceeded, a licence
// ceiling that is not a temporary condition at all: retrying it can only turn
// one immediate refusal into four delayed ones.
//
// DeadlineExceeded is worse per attempt. A generation that hit the server's
// timeout has already spent worker_pool.generation_timeout — two minutes by
// default — running a plugin process. Retrying spends it again, and the client
// deadline that survives the first attempt rarely survives the third; the usual
// outcome was the same failure, three times as slowly, with three times the
// work done on the server.
//
// A caller who wants either retried can say so with WithUnaryInterceptor.
func isTransient(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}

	return st.Code() == codes.Unavailable
}

// backoffDelay calculates the delay for the given attempt using exponential
// backoff with random jitter up to 25%.
//
//	delay = min(baseDelay * 2^attempt + jitter, maxDelay)
//
// The shift is capped before it is taken. WithMaxRetries is a public option
// with no upper bound, and `baseDelay << attempt` overflows int64 well before
// the attempt counter looks unreasonable: at the default 100ms base, attempt 40
// produced a negative duration, whose quarter is negative, and rand.Int64N
// panics on a non-positive argument — so sdk.WithMaxRetries(41) plus one
// retryable error crashed the caller from inside an interceptor.
func backoffDelay(baseDelay, maxDelay time.Duration, attempt int) time.Duration {
	delay := maxDelay

	// Past this the shift cannot stay positive for any base worth using, and
	// the result would be clamped to maxDelay regardless.
	const maxShift = 62

	if attempt >= 0 && attempt < maxShift {
		shifted := baseDelay << attempt // baseDelay * 2^attempt
		if shifted > 0 && shifted < maxDelay {
			delay = shifted
		}
	}

	// Add jitter: random value up to 25% of current delay.
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1)) //nolint:gosec,mnd // jitter is not security-sensitive; /4 = 25%
	delay += jitter

	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}
