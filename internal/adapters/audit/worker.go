package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/safe"
)

// Loss reasons reported through the audit_events_lost_total counter.
const (
	reasonEnqueueTimeout  = "enqueue_timeout"
	reasonSaveFailed      = "save_failed"
	reasonShutdownTimeout = "shutdown_timeout"
)

const (
	labelReason = "reason"

	// retryBaseDelay is the first back-off step; each retry doubles it.
	retryBaseDelay = 100 * time.Millisecond

	// defaultEnqueueTimeout bounds how long a caller waits for room in the
	// queue. A healthy worker drains at least BatchSize/FlushInterval entries a
	// second — a hundred, at the shipped defaults — so this expires only when
	// the queue is genuinely backed up, never in normal operation.
	defaultEnqueueTimeout = time.Second

	// defaultFlushTimeout bounds one write to storage, retries included. A
	// batch that cannot be written inside it is lost and counted; without a
	// bound a hung database would stall the worker and back the queue up behind
	// it.
	defaultFlushTimeout = 5 * time.Second
)

// ErrWorkerStopped is returned when a batch cannot be retried because the
// worker is shutting down.
var ErrWorkerStopped = errors.New("audit worker stopped")

// Config configures the audit worker.
type Config struct {
	// BufferSize is the capacity of the queue between Core and the worker.
	BufferSize int
	// BatchSize is how many entries are written in a single statement.
	BatchSize int
	// FlushInterval forces a write of a partially filled batch.
	FlushInterval time.Duration
	// MaxSaveRetries is how many extra attempts a failing batch gets.
	MaxSaveRetries int
	// EnqueueTimeout bounds how long Send waits for room in the queue before
	// giving up on an entry. Zero means Send never waits: an entry that finds
	// the queue full is dropped and counted immediately. Negative selects
	// defaultEnqueueTimeout.
	EnqueueTimeout time.Duration
	// FlushTimeout bounds a single write to storage. Zero selects
	// defaultFlushTimeout.
	FlushTimeout time.Duration
	// ShutdownFlushTimeout bounds the final write performed after the queue
	// closes. It must be smaller than the timeout passed to Shutdown, so the
	// write can finish before Shutdown gives up on it. Zero selects
	// defaultFlushTimeout.
	ShutdownFlushTimeout time.Duration
}

// Worker reads audit events from a queue and writes them to storage in
// batches. It implements core.AuditSink.
type Worker struct {
	workerMetrics

	store  core.AuditLog
	cfg    Config
	logger *slog.Logger

	entries chan core.AuditEntry
	done    chan struct{}

	// batch holds entries accepted but not yet written. Owned exclusively by
	// Run; pending mirrors its length so Shutdown can read it across goroutines.
	batch   []core.AuditEntry
	pending atomic.Int64

	guard  *safe.Guard
	tracer trace.Tracer
}

var _ core.AuditSink = (*Worker)(nil)

// workerMetrics is what the worker reports about itself. It is a type of its
// own so that constructing five collectors does not sit in the middle of
// NewWorker, where it buried the part that actually decides how the worker
// behaves; embedded, so every w.eventsLost in this file still reads as one.
type workerMetrics struct {
	eventsLost    *prometheus.CounterVec
	eventsSkipped prometheus.Counter
	saveFailures  prometheus.Counter
	batchSize     prometheus.Histogram
}

// newWorkerMetrics builds the collectors and registers them on reg, which may
// be nil in tests. queue is read for the depth gauge, which is a GaugeFunc
// rather than a number the worker has to remember to update.
func newWorkerMetrics(reg *prometheus.Registry, namespace string, queue <-chan core.AuditEntry) workerMetrics {
	metrics := workerMetrics{
		eventsLost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "audit_events_lost_total",
			Help:      "Total number of audit events lost, by reason.",
		}, []string{labelReason}),

		eventsSkipped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "audit_events_skipped_total",
			Help:      "Total number of audit events not recorded because the license does not include audit.",
		}),

		saveFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "audit_save_failures_total",
			Help:      "Total number of failed audit write attempts, including those a retry later recovered.",
		}),

		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "audit_batch_size",
			Help:      "Number of audit entries written per batch.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 10), //nolint:mnd // 1..512 entries
		}),
	}

	queueDepth := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "audit_queue_depth",
		Help:      "Current number of audit events in the queue.",
	}, func() float64 {
		return float64(len(queue))
	})

	// Pre-create every reason series so a zero value is visible before the
	// first loss ever happens.
	for _, reason := range []string{reasonEnqueueTimeout, reasonSaveFailed, reasonShutdownTimeout} {
		metrics.eventsLost.WithLabelValues(reason)
	}

	if reg != nil {
		reg.MustRegister(queueDepth, metrics.eventsLost, metrics.eventsSkipped,
			metrics.saveFailures, metrics.batchSize)
	}

	return metrics
}

// NewWorker creates a worker with a buffered queue. Pass a nil registry to
// skip metric registration, which keeps the worker usable in tests.
func NewWorker(
	store core.AuditLog, cfg Config, logger *slog.Logger,
	reg *prometheus.Registry, namespace string,
) *Worker {
	// EnqueueTimeout is left alone at zero. "Drop the entry rather than wait for
	// room" is a legitimate choice for a deployment that would rather lose an
	// audit record than slow a request down, it is reachable only by writing it
	// down, and the field's struct tag gives everyone else 1s.
	if cfg.EnqueueTimeout < 0 {
		cfg.EnqueueTimeout = defaultEnqueueTimeout
	}

	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultFlushTimeout
	}

	if cfg.ShutdownFlushTimeout <= 0 {
		cfg.ShutdownFlushTimeout = defaultFlushTimeout
	}

	ch := make(chan core.AuditEntry, cfg.BufferSize)

	return &Worker{
		store:         store,
		cfg:           cfg,
		logger:        logger,
		entries:       ch,
		done:          make(chan struct{}),
		batch:         make([]core.AuditEntry, 0, cfg.BatchSize),
		workerMetrics: newWorkerMetrics(reg, namespace, ch),
		guard:         safe.NewGuard(reg, namespace),
		tracer:        otel.Tracer("audit"),
	}
}

// Send implements core.AuditSink. It blocks until the entry is queued or
// EnqueueTimeout passes; an entry that never found room is a lost event and is
// counted.
//
// The caller's cancellation is deliberately dropped. The entry is already
// built, so queueing it needs nothing from the request, and riding the request
// context made an entry race its own cancellation: with room in the queue both
// select cases are ready at once and Go picks between them at random, losing
// about half the records of any cancelled call. Graceful shutdown made that
// systematic rather than rare — it cancels this context while handlers are
// still running, so every rollout dropped a share of what it audited.
//
// Values survive WithoutCancel, so the trace and the actor still travel with
// the entry.
func (w *Worker) Send(ctx context.Context, entry core.AuditEntry) {
	// A zero timeout cannot go through context.WithTimeout: its Done channel is
	// closed from birth, and a select whose queue has room then has two ready
	// cases and picks between them at random — the same coin flip described
	// above, dropping entries the queue had space for.
	if w.cfg.EnqueueTimeout == 0 {
		select {
		case w.entries <- entry:
		default:
			w.eventsLost.WithLabelValues(reasonEnqueueTimeout).Inc()
			w.logger.Warn("audit queue full, entry dropped", "entry_id", entry.ID, "operation", entry.OperationType)
		}

		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.EnqueueTimeout)
	defer cancel()

	select {
	case w.entries <- entry:
	case <-ctx.Done():
		w.eventsLost.WithLabelValues(reasonEnqueueTimeout).Inc()
		w.logger.Warn("audit enqueue timed out", "entry_id", entry.ID, "operation", entry.OperationType)
	}
}

// Skipped implements core.AuditSink.
func (w *Worker) Skipped() {
	w.eventsSkipped.Inc()
}

// Run consumes the queue until it is closed, writing entries in batches.
// Blocks until the queue is drained.
func (w *Worker) Run(ctx context.Context) {
	defer close(w.done)

	// Every write runs on a context detached from the application's. The select
	// below has no ctx.Done() case — Run ends when the queue closes, not when
	// the context is cancelled — so ctx serves here purely as the write
	// context, and leaving it attached meant every flush after SIGTERM reached
	// ExecContext already cancelled and lost its whole batch. That window lasts
	// as long as graceful shutdown does, up to the generation timeout, at one
	// flush per FlushInterval.
	//
	// Each flush still gets a deadline of its own below, so detaching bounds
	// nothing less than it did before.
	base := context.WithoutCancel(ctx)

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case entry, ok := <-w.entries:
			if !ok {
				// Queue closed by Shutdown. This last write gets its own budget:
				// it is the one the caller is waiting on.
				w.flushWithin(base, w.cfg.ShutdownFlushTimeout)

				return
			}

			w.batch = append(w.batch, entry)
			w.pending.Store(int64(len(w.batch)))

			if len(w.batch) >= w.cfg.BatchSize {
				w.flushWithin(base, w.cfg.FlushTimeout)
			}
		case <-ticker.C:
			w.flushWithin(base, w.cfg.FlushTimeout)
		}
	}
}

// flushWithin runs flush under a deadline of its own, derived from base rather
// than from any caller's context.
func (w *Worker) flushWithin(base context.Context, timeout time.Duration) { //nolint:funcorder // helper called by Run above it
	if len(w.batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	w.flush(ctx)
}

// flush writes the accumulated batch and clears it. The batch is cleared
// whether or not the write succeeded — a batch that survived its retries is
// unrecoverable and must not block the ones behind it.
//
// The barrier is here, around the write, rather than around Run: a panic caught
// outside the loop would leave the process running with audit silently switched
// off, and audit is an Enterprise commitment, so stopping quietly is the one
// outcome that must not happen.
//
// Placing it here is also what keeps a failed batch from repeating. Were the
// barrier outside flush, a panic would skip the clearing below and hand the same
// entries to the next tick, and the one after — an endless panic loop that never
// drains the queue. Clearing is deferred so that stays true if the barrier ever
// moves.
func (w *Worker) flush(ctx context.Context) { //nolint:funcorder // flush is a helper called by Run above it
	if len(w.batch) == 0 {
		return
	}

	defer func() {
		w.batch = w.batch[:0]
		w.pending.Store(0)
	}()

	w.guard.Do(ctx, "audit.flush", func() {
		err := w.saveWithRetry(ctx, w.batch)
		if err != nil {
			w.eventsLost.WithLabelValues(reasonSaveFailed).Add(float64(len(w.batch)))
			w.logger.Error("audit batch lost", "error", err, "count", len(w.batch))

			return
		}

		w.batchSize.Observe(float64(len(w.batch)))
	})
}

// saveWithRetry writes a batch, retrying transient failures with exponential
// back-off. Shutdown cancels the context, which ends the retry loop.
func (w *Worker) saveWithRetry(ctx context.Context, entries []core.AuditEntry) error { //nolint:funcorder // helper of flush
	ctx, span := w.tracer.Start(ctx, "audit.saveBatch",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.Int("audit.batch_size", len(entries)),
		))
	defer span.End()

	delay := retryBaseDelay

	var err error

	for attempt := 0; attempt <= w.cfg.MaxSaveRetries; attempt++ {
		err = w.store.SaveBatch(ctx, entries)
		if err == nil {
			return nil
		}

		w.saveFailures.Inc()
		span.RecordError(err)

		if attempt == w.cfg.MaxSaveRetries {
			break
		}

		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			span.SetStatus(codes.Error, ErrWorkerStopped.Error())

			return ErrWorkerStopped
		}
	}

	span.SetStatus(codes.Error, err.Error())

	return fmt.Errorf("w.store.SaveBatch: %w", err)
}

// Shutdown closes the queue and waits for the worker to drain it.
// Returns the number of events lost, counting both the queue remainder and any
// batch still held in memory.
func (w *Worker) Shutdown(timeout time.Duration) int {
	close(w.entries)

	select {
	case <-w.done:
		return 0
	case <-time.After(timeout):
		lost := len(w.entries) + int(w.pending.Load())
		w.eventsLost.WithLabelValues(reasonShutdownTimeout).Add(float64(lost))
		w.logger.Warn("audit shutdown timeout, events lost", "count", lost)

		return lost
	}
}
