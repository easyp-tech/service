package audit

import (
	"context"
	"errors"
	"io"
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

func (s *fakeStore) SaveBatch(_ context.Context, entries []core.AuditEntry) error {
	s.calls.Add(1)

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
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig() Config {
	return Config{
		BufferSize:           64,
		BatchSize:            3,
		FlushInterval:        time.Hour, // effectively disabled unless a test wants it
		MaxSaveRetries:       0,
		ShutdownFlushTimeout: time.Second,
	}
}

func entry(op string) core.AuditEntry {
	return core.AuditEntry{OperationType: op, Status: core.AuditStatusSuccess, CreatedAt: time.Now()}
}

// counterValue reads a single counter's value.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()

	var m dto.Metric
	require.NoError(t, c.Write(&m))

	return m.GetCounter().GetValue()
}

// lostValue reads the lost-events counter for one reason.
func lostValue(t *testing.T, w *Worker, reason string) float64 {
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
		wantLost   float64
		// wantFailures is how many attempts are expected to have failed.
		wantFailures float64
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

func TestWorkerSendCancelledIsCounted(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.BufferSize = 1

	store := newFakeStore()
	worker := NewWorker(store, cfg, discardLogger(), nil, "test")

	// Run is never started, so the queue fills and the send blocks until the
	// context is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	worker.Send(ctx, entry(core.OperationGenerateCode))

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	worker.Send(ctx, entry(core.OperationGenerateCode))

	assert.Equal(t, float64(1), lostValue(t, worker, reasonSendCancelled),
		"an event dropped because the context was cancelled must be counted as lost")
}

func TestWorkerSkippedIsCounted(t *testing.T) {
	t.Parallel()

	worker := NewWorker(newFakeStore(), testConfig(), discardLogger(), nil, "test")

	worker.Skipped()
	worker.Skipped()

	assert.Equal(t, float64(2), counterValue(t, worker.eventsSkipped))
	assert.Zero(t, lostValue(t, worker, reasonSaveFailed),
		"a licence-skipped event is normal behaviour, not a loss")
}
