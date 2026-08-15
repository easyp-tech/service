package database

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/monitor"
)

// quiet returns a context whose logger discards. waitForDB reports every failed
// attempt, and the failure cases below make many of them on purpose.
func quiet(t *testing.T) context.Context {
	t.Helper()

	return monitor.WithContext(t.Context(), slog.New(slog.DiscardHandler))
}

var errUnreachable = errors.New("connection refused")

// countingPinger fails the first failures pings and counts every call.
type countingPinger struct {
	failures int64
	calls    atomic.Int64
}

func (p *countingPinger) PingContext(context.Context) error {
	if p.calls.Add(1) <= p.failures {
		return errUnreachable
	}

	return nil
}

func TestWaitForDBReturnsImmediatelyWhenReachable(t *testing.T) {
	t.Parallel()

	p := &countingPinger{failures: 0} //nolint:exhaustruct // Zero value is the counter.

	require.NoError(t, waitForDB(quiet(t), p))
	require.Equal(t, int64(1), p.calls.Load(), "a reachable database must not be polled twice")
}

func TestWaitForDBRetriesUntilReachable(t *testing.T) {
	t.Parallel()

	p := &countingPinger{failures: 3} //nolint:exhaustruct // Zero value is the counter.

	require.NoError(t, waitForDB(quiet(t), p))
	require.Equal(t, int64(4), p.calls.Load())
}

// The regression this file exists for. The previous implementation retried with
// no delay whatsoever, so a service started against a database that was down
// spun a core and hammered it with connection attempts. Returning on deadline
// is not the thing being tested — the old code did that too. The attempt count
// is: with backoff it stays in single digits, without it reaches thousands.
func TestWaitForDBBacksOffInsteadOfSpinning(t *testing.T) {
	t.Parallel()

	const budget = 700 * time.Millisecond

	p := &countingPinger{failures: 1 << 30} //nolint:exhaustruct // Never succeeds.

	ctx, cancel := context.WithTimeout(quiet(t), budget)
	defer cancel()

	start := time.Now()
	err := waitForDB(ctx, p)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, errUnreachable, "the reason waiting failed must survive in the error")

	// 100ms + 200ms + 400ms already exceeds the budget, so four attempts is the
	// ceiling. Ten leaves room for scheduling noise while still being three
	// orders of magnitude below what a spinning loop produces.
	require.LessOrEqual(t, p.calls.Load(), int64(10),
		"too many attempts for the elapsed time; the backoff is not being applied")

	require.Less(t, elapsed, budget+pingBackoffCeiling,
		"waiting outlived its context by more than one backoff interval")
}

// A context already cancelled must not buy a delay's worth of waiting first.
func TestWaitForDBHonoursAlreadyCancelledContext(t *testing.T) {
	t.Parallel()

	p := &countingPinger{failures: 1 << 30} //nolint:exhaustruct // Never succeeds.

	ctx, cancel := context.WithCancel(quiet(t))
	cancel()

	start := time.Now()
	err := waitForDB(ctx, p)

	require.Error(t, err)
	require.Less(t, time.Since(start), pingBackoffInitial,
		"a cancelled context still slept through a backoff interval")
}
