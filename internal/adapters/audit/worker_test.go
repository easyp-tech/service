package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

var errSaveFailed = errors.New("save failed")

// fakeStore is an in-memory core.AuditLog used in tests.
type fakeStore struct {
	mu      sync.Mutex
	batches [][]core.AuditEntry

	calls atomic.Int64
	// failTimes is how many leading calls return failWith.
	failTimes atomic.Int64
	failWith  error

	// saved is signalled after every successful batch.
	saved chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{saved: make(chan struct{}, 128)}
}

// SaveBatch honours the context the way the real store does: it reaches
// db.ExecContext, which fails immediately on a cancelled one. A fake that
// ignored the context would let a flush wired to a dead context look healthy
// here and lose every batch in production.
func (s *fakeStore) SaveBatch(ctx context.Context, entries []core.AuditEntry) error {
	s.calls.Add(1)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake store: %w", err)
	}

	if s.failTimes.Load() > 0 {
		s.failTimes.Add(-1)

		return s.failWith
	}

	s.mu.Lock()
	batch := make([]core.AuditEntry, len(entries))
	copy(batch, entries)
	s.batches = append(s.batches, batch)
	s.mu.Unlock()

	select {
	case s.saved <- struct{}{}:
	default:
	}

	return nil
}

func (s *fakeStore) snapshot() [][]core.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([][]core.AuditEntry, len(s.batches))
	copy(out, s.batches)

	return out
}

func (s *fakeStore) totalSaved() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int
	for _, batch := range s.batches {
		n += len(batch)
	}

	return n
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func testConfig() Config {
	return Config{
		BufferSize:     64,
		BatchSize:      3,
		FlushInterval:  time.Hour, // effectively disabled unless a test wants it
		MaxSaveRetries: 0,
		// Short so a test that deliberately fills the queue does not spend the
		// production second waiting for it.
		EnqueueTimeout:       50 * time.Millisecond,
		FlushTimeout:         time.Second,
		ShutdownFlushTimeout: time.Second,
	}
}

func entry(op string) core.AuditEntry {
	return core.AuditEntry{OperationType: op, Status: core.AuditStatusSuccess, CreatedAt: time.Now()}
}

// counterValue reads a counter as an int. Every counter here holds an event
// count, so an exact integer comparison is the meaningful one.
func counterValue(t *testing.T, c prometheus.Counter) int {
	t.Helper()

	var m dto.Metric
	require.NoError(t, c.Write(&m))

	return int(m.GetCounter().GetValue())
}

// lostValue reads the lost-events counter for one reason.
func lostValue(t *testing.T, w *Worker, reason string) int {
	t.Helper()

	return counterValue(t, w.eventsLost.WithLabelValues(reason))
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}

		time.Sleep(time.Millisecond)
	}

	return cond()
}

func TestWorkerFlushesWhenBatchFills(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	worker := NewWorker(store, testConfig(), discardLogger(), nil, "test")

	ctx := t.Context()
	go worker.Run(ctx)

	for range 3 {
		worker.Send(ctx, entry(core.OperationGenerateCode))
	}

	require.True(t, waitFor(t, func() bool { return store.totalSaved() == 3 }),
		"batch should be written once it reaches BatchSize without waiting for the timer")

	batches := store.snapshot()
	require.Len(t, batches, 1)
	assert.Len(t, batches[0], 3)

	assert.Zero(t, worker.Shutdown(time.Second))
}

func TestWorkerFlushesOnTimer(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.FlushInterval = 10 * time.Millisecond

	store := newFakeStore()
	worker := NewWorker(store, cfg, discardLogger(), nil, "test")

	ctx := t.Context()
	go worker.Run(ctx)

	// One entry, well below BatchSize: only the timer can flush it.
	worker.Send(ctx, entry(core.OperationListPlugins))

	require.True(t, waitFor(t, func() bool { return store.totalSaved() == 1 }),
		"a partially filled batch should be written by the flush timer")

	assert.Zero(t, worker.Shutdown(time.Second))
}

func TestWorkerRetriesFailedBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		maxRetries int
		failTimes  int64
		wantSaved  int
		wantLost   int
		// wantFailures is how many attempts are expected to have failed.
		wantFailures int
	}{
		{
			name:         "recovers_on_second_attempt",
			maxRetries:   3,
			failTimes:    1,
			wantSaved:    3,
			wantLost:     0,
			wantFailures: 1,
		},
		{
			name:         "gives_up_after_retries_are_exhausted",
			maxRetries:   2,
			failTimes:    99,
			wantSaved:    0,
			wantLost:     3,
			wantFailures: 3, // initial attempt + 2 retries
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig()
			cfg.MaxSaveRetries = tt.maxRetries

			store := newFakeStore()
			store.failWith = errSaveFailed
			store.failTimes.Store(tt.failTimes)

			worker := NewWorker(store, cfg, discardLogger(), nil, "test")

			ctx := t.Context()
			go worker.Run(ctx)

			for range 3 {
				worker.Send(ctx, entry(core.OperationCreatePlugin))
			}

			require.True(t, waitFor(t, func() bool {
				return counterValue(t, worker.saveFailures) >= tt.wantFailures
			}), "expected %v failed attempts", tt.wantFailures)

			require.True(t, waitFor(t, func() bool {
				return store.totalSaved() == tt.wantSaved &&
					lostValue(t, worker, reasonSaveFailed) == tt.wantLost
			}), "expected %d saved and %v lost", tt.wantSaved, tt.wantLost)

			assert.Equal(t, tt.wantFailures, counterValue(t, worker.saveFailures))
		})
	}
}

func TestWorkerShutdownFlushesPendingBatch(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	worker := NewWorker(store, testConfig(), discardLogger(), nil, "test")

	// A cancelled context is the realistic shutdown case: the signal handler
	// cancels before the deferred Shutdown runs. The final write must still
	// go through.
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)

	// Two entries: below BatchSize, and the flush timer is an hour away.
	worker.Send(ctx, entry(core.OperationDeletePlugin))
	worker.Send(ctx, entry(core.OperationUpdatePlugin))

	cancel()

	assert.Zero(t, worker.Shutdown(2*time.Second), "no events should be lost on shutdown")
	assert.Equal(t, 2, store.totalSaved())
}

func TestWorkerEnqueueTimeoutIsCounted(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.BufferSize = 1

	store := newFakeStore()
	worker := NewWorker(store, cfg, discardLogger(), nil, "test")

	// Run is never started, so the one queue slot fills and the second send has
	// nowhere to go. This is the only loss Send is allowed to cause: real
	// pressure, not a cancelled caller.
	ctx := t.Context()
	worker.Send(ctx, entry(core.OperationGenerateCode))

	start := time.Now()
	worker.Send(ctx, entry(core.OperationGenerateCode))
	waited := time.Since(start)

	assert.Equal(t, 1, lostValue(t, worker, reasonEnqueueTimeout),
		"an event dropped because the queue stayed full must be counted as lost")
	assert.GreaterOrEqual(t, waited, cfg.EnqueueTimeout,
		"Send should wait out the enqueue timeout before giving up")
	assert.Less(t, waited, time.Second,
		"Send should give up at the timeout rather than block indefinitely")
}

// A cancelled caller must not cost the entry its place in the queue.
//
// Send used to select on the request context alongside the queue write. With
// room in the queue both cases are ready at once, and Go picks between ready
// cases at random — so a cancelled call lost about half of its audit records,
// measured at 478-515 of every 1000. The count here is what makes that visible:
// a single send would pass the old code half the time.
//
// This matters most on shutdown, where the application context is cancelled
// while handlers still run, making the loss systematic on every rollout rather
// than occasional.
func TestSendIgnoresCallerCancellation(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.BufferSize = 1000

	worker := NewWorker(newFakeStore(), cfg, discardLogger(), nil, "test")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const sends = 1000
	for range sends {
		worker.Send(ctx, entry(core.OperationGenerateCode))
	}

	assert.Len(t, worker.entries, sends,
		"every entry should be queued: the queue was empty, so nothing but the caller's cancellation could have dropped one")
	assert.Zero(t, lostValue(t, worker, reasonEnqueueTimeout),
		"a cancelled caller is not queue pressure and must not be counted as such")
}

// The flush path must not inherit the application's cancellation either.
//
// Run ends when the queue closes, not when the context is cancelled, so the
// context it is handed serves purely as the write context. Left attached, every
// timer flush between SIGTERM and the queue closing reached ExecContext already
// cancelled and lost its whole batch — for as long as graceful shutdown took.
func TestTimerFlushSurvivesCancelledParentContext(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.FlushInterval = 10 * time.Millisecond

	store := newFakeStore()
	worker := NewWorker(store, cfg, discardLogger(), nil, "test")

	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)

	// Cancel first, then produce: every flush from here on runs under a context
	// that is already dead.
	cancel()

	worker.Send(ctx, entry(core.OperationListPlugins))

	require.True(t, waitFor(t, func() bool { return store.totalSaved() == 1 }),
		"the timer flush should reach storage even though the parent context is cancelled")

	assert.Zero(t, lostValue(t, worker, reasonSaveFailed))
	assert.Zero(t, worker.Shutdown(2*time.Second))
}

// Send must never outlive the queue it writes to.
//
// Nothing in the running service can reach this: Shutdown closes the queue from
// a defer that runs only after serveApp returns, and serveApp returns only once
// GracefulStop and srv.Shutdown(context.Background()) have waited out every
// handler. That invariant rests on serve.HTTP passing an uncancellable context
// to Shutdown, which is easy to lose in an unrelated edit — hence a test rather
// than a comment.
func TestShutdownDoesNotRaceSend(t *testing.T) {
	t.Parallel()

	worker := NewWorker(newFakeStore(), testConfig(), discardLogger(), nil, "test")

	ctx := t.Context()
	go worker.Run(ctx)

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 50 {
				worker.Send(ctx, entry(core.OperationGenerateCode))
			}
		}()
	}

	wg.Wait()
	worker.Shutdown(2 * time.Second)
}

func TestWorkerSkippedIsCounted(t *testing.T) {
	t.Parallel()

	worker := NewWorker(newFakeStore(), testConfig(), discardLogger(), nil, "test")

	worker.Skipped()
	worker.Skipped()

	assert.Equal(t, 2, counterValue(t, worker.eventsSkipped))
	assert.Zero(t, lostValue(t, worker, reasonSaveFailed),
		"a licence-skipped event is normal behaviour, not a loss")
}
