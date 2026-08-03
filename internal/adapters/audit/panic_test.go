package audit_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/adapters/audit"
	"github.com/easyp-tech/service/internal/core"
)

// panickingStore panics on the first n writes and records every batch it was
// handed, so a test can see both that the worker recovered and what it was
// asked to write afterwards.
type panickingStore struct {
	mu         sync.Mutex
	panicsLeft int
	batches    [][]core.AuditEntry
}

func (s *panickingStore) SaveBatch(_ context.Context, entries []core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.panicsLeft > 0 {
		s.panicsLeft--

		panic("audit storage blew up")
	}

	saved := make([]core.AuditEntry, len(entries))
	copy(saved, entries)
	s.batches = append(s.batches, saved)

	return nil
}

func (s *panickingStore) written() [][]core.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([][]core.AuditEntry, len(s.batches))
	copy(out, s.batches)

	return out
}

func newWorker(t *testing.T, store core.AuditLog) *audit.Worker {
	t.Helper()

	w := audit.NewWorker(store, audit.Config{
		BufferSize:           16,
		BatchSize:            1,
		FlushInterval:        20 * time.Millisecond,
		MaxSaveRetries:       0,
		ShutdownFlushTimeout: 500 * time.Millisecond,
	}, slog.New(slog.DiscardHandler), prometheus.NewRegistry(), "easyp")

	go w.Run(t.Context())

	return w
}

// The failure this guards is subtle: a panic inside the write leaves the batch
// in place, so without deferred clearing the next tick retries the same entries
// and panics again — forever, never draining the queue.
func TestPoisonedBatchDoesNotRepeatForever(t *testing.T) {
	t.Parallel()

	store := &panickingStore{panicsLeft: 1} //nolint:exhaustruct // Zero values are fine.
	w := newWorker(t, store)

	w.Send(t.Context(), core.AuditEntry{OperationType: "first"})  //nolint:exhaustruct // Only the type is read.
	w.Send(t.Context(), core.AuditEntry{OperationType: "second"}) //nolint:exhaustruct // Only the type is read.

	require.Eventually(t, func() bool {
		return len(store.written()) > 0
	}, 3*time.Second, 10*time.Millisecond, "audit stopped writing after a panic")

	written := store.written()

	// The poisoned entry is gone, not retried. Losing it is the documented
	// behaviour for a batch that cannot be written; repeating it is not.
	for _, batch := range written {
		for _, entry := range batch {
			require.NotEqual(t, "first", entry.OperationType,
				"the batch that panicked was handed back to storage again")
		}
	}

	require.Zero(t, w.Shutdown(2*time.Second), "entries were lost on shutdown")
}

// A worker that catches the panic but stops consuming is no better than one
// that crashed: the queue fills, Send blocks, and every request that writes an
// audit entry stalls behind it.
func TestWorkerKeepsDrainingAfterAPanic(t *testing.T) {
	t.Parallel()

	store := &panickingStore{panicsLeft: 3} //nolint:exhaustruct // Zero values are fine.
	w := newWorker(t, store)

	for range 3 {
		w.Send(t.Context(), core.AuditEntry{OperationType: "bad"}) //nolint:exhaustruct // Only the type is read.
	}

	w.Send(t.Context(), core.AuditEntry{OperationType: "good"}) //nolint:exhaustruct // Only the type is read.

	require.Eventually(t, func() bool {
		for _, batch := range store.written() {
			for _, entry := range batch {
				if entry.OperationType == "good" {
					return true
				}
			}
		}

		return false
	}, 3*time.Second, 10*time.Millisecond, "the worker never wrote again after three panics")

	require.Zero(t, w.Shutdown(2*time.Second), "entries were lost on shutdown")
}
