package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grafana/pyroscope-go"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/safe"
)

const (
	defaultGenerationTimeout        = 120 * time.Second
	defaultShutdownTimeout          = 30 * time.Second
	defaultMaxConcurrentGenerations = 16
)

// WorkerPoolConfig содержит параметры конфигурации worker pool.
//
// Workers ограничивает поиск плагина (БД и, при промахе, скачивание из
// хранилища), MaxConcurrentGenerations — запуск самих процессов плагинов.
// Это разные ресурсы: первое упирается в сеть, второе в CPU и память.
type WorkerPoolConfig struct {
	Workers                  int
	QueueSize                int
	MaxConcurrentGenerations int
	GenerationTimeout        time.Duration
	MaxRetries               int
	ShutdownTimeout          time.Duration
}

// job представляет единицу работы для воркера.
type job struct {
	ctx           context.Context
	pluginGroup   string
	pluginName    string
	pluginVersion string
	result        chan<- jobResult
}

// jobResult содержит результат обработки задания.
type jobResult struct {
	plugin Plugin
	err    error
}

// WorkerPool управляет пулом горутин для ограничения параллелизма выполнения плагинов.
// Реализует интерфейс Registry, оборачивая реальный Registry.
type WorkerPool struct {
	inner         Registry
	jobs          chan job
	cfg           WorkerPoolConfig
	logger        *slog.Logger
	metrics       Metrics
	wg            sync.WaitGroup
	closed        atomic.Bool
	activeWorkers prometheus.Gauge
	rejectedTotal prometheus.Counter
	jobsTotal     prometheus.Counter
	tracer        trace.Tracer
	guard         *safe.Guard

	// gen ограничивает одновременный запуск процессов плагинов. Воркеры для
	// этого не годятся: воркер освобождается, отдав plugin в канал, а Generate
	// вызывается уже горутиной вызывающего.
	gen *genLimiter
}

// genLimiter пропускает не более cap одновременных генераций, держит очередь
// глубиной queue и отклоняет всё сверх неё.
type genLimiter struct {
	slots   *semaphore.Weighted
	queue   int64
	waiting atomic.Int64

	active   prometheus.Gauge
	inQueue  prometheus.Gauge
	rejected prometheus.Counter
}

// poolPlugin оборачивает реальный Plugin, добавляя таймаут и retry при вызове Generate.
type poolPlugin struct {
	inner   Plugin
	cfg     WorkerPoolConfig
	logger  *slog.Logger
	metrics Metrics
	gen     *genLimiter
}

// NewWorkerPool создаёт WorkerPool с нормализованной конфигурацией.
func NewWorkerPool(
	inner Registry, cfg WorkerPoolConfig, logger *slog.Logger,
	metrics Metrics, reg *prometheus.Registry, namespace string,
) *WorkerPool {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.QueueSize < 0 {
		cfg.QueueSize = 0
	}
	if cfg.GenerationTimeout == 0 {
		cfg.GenerationTimeout = defaultGenerationTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.MaxConcurrentGenerations < 1 {
		cfg.MaxConcurrentGenerations = defaultMaxConcurrentGenerations
	}

	jobs := make(chan job, cfg.QueueSize)

	activeWorkers := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "pool_active_workers",
		Help:      "Number of workers currently processing jobs.",
	})

	rejectedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "pool_rejected_total",
		Help:      "Total number of jobs rejected due to full queue.",
	})

	jobsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "pool_jobs_total",
		Help:      "Total number of jobs accepted into the queue.",
	})

	queueDepth := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "pool_queue_depth",
		Help:      "Current number of jobs in the worker pool queue.",
	}, func() float64 {
		return float64(len(jobs))
	})

	if reg != nil {
		reg.MustRegister(queueDepth, activeWorkers, rejectedTotal, jobsTotal)
	}

	return &WorkerPool{
		inner:         inner,
		jobs:          jobs,
		cfg:           cfg,
		logger:        logger,
		metrics:       metrics,
		activeWorkers: activeWorkers,
		rejectedTotal: rejectedTotal,
		jobsTotal:     jobsTotal,
		tracer:        otel.Tracer("pool"),
		gen:           newGenLimiter(cfg, reg, namespace),
		guard:         safe.NewGuard(reg, namespace),
	}
}

// newGenLimiter собирает ограничитель одновременных генераций вместе с его
// метриками.
func newGenLimiter(cfg WorkerPoolConfig, reg *prometheus.Registry, namespace string) *genLimiter {
	active := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "pool_generations_active",
		Help:      "Number of plugin processes currently running.",
	})

	inQueue := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "pool_generations_waiting",
		Help:      "Number of requests waiting for a generation slot.",
	})

	rejected := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "pool_generations_rejected_total",
		Help:      "Total generations rejected because the wait queue was full.",
	})

	if reg != nil {
		reg.MustRegister(active, inQueue, rejected)
	}

	return &genLimiter{
		slots:    semaphore.NewWeighted(int64(cfg.MaxConcurrentGenerations)),
		queue:    int64(cfg.QueueSize),
		active:   active,
		inQueue:  inQueue,
		rejected: rejected,
	}
}

// acquire занимает слот генерации. Возвращает функцию освобождения либо
// ErrServerOverloaded, если очередь ожидания уже заполнена.
//
// Семантика намеренно повторяет очередь заданий пула: пускаем до предела,
// дальше ограниченная очередь, дальше явный отказ. Молча копить ожидающих
// нельзя — они держат соединения и память.
func (g *genLimiter) acquire(ctx context.Context) (func(), error) {
	if !g.slots.TryAcquire(1) {
		if g.waiting.Add(1) > g.queue {
			g.waiting.Add(-1)
			g.rejected.Inc()

			return nil, ErrServerOverloaded
		}

		g.inQueue.Set(float64(g.waiting.Load()))
		err := g.slots.Acquire(ctx, 1)
		g.inQueue.Set(float64(g.waiting.Add(-1)))

		if err != nil {
			return nil, fmt.Errorf("waiting for a generation slot: %w", err)
		}
	}

	g.active.Inc()

	return func() {
		g.active.Dec()
		g.slots.Release(1)
	}, nil
}

// Start запускает N горутин-воркеров.
func (p *WorkerPool) Start(ctx context.Context) {
	for range p.cfg.Workers {
		p.wg.Add(1)
		go p.worker(ctx)
	}
}

func (p *WorkerPool) worker(ctx context.Context) { //nolint:funcorder // worker and processJob are implementation details of the pool
	defer p.wg.Done()
	for j := range p.jobs {
		p.processJob(ctx, j)
	}
}

// processJob resolves one plugin and answers the caller waiting on work.result.
//
// The panic barrier sits here rather than around the worker loop on purpose.
// Recovering in worker would end that goroutine, so the pool would quietly shed
// one worker per panic until it had none left — a process that is alive and
// answers nothing, which takes far longer to diagnose than the crash it
// replaced. Recovering per job leaves the worker free to take the next one.
//
// Locating a plugin means a database read, a download from object storage and a
// tar extraction, all driven by an artifact somebody else built. This is the
// path a malformed archive takes, and until this barrier existed it took the
// whole process down with it.
func (p *WorkerPool) processJob(_ context.Context, work job) { //nolint:funcorder,lll // worker and processJob are implementation details of the pool
	p.activeWorkers.Inc()
	defer p.activeWorkers.Dec()

	// answered guards against replying twice: the happy path already sent, and
	// a panic after that must not send again. The channel is buffered for one.
	answered := false
	answer := func(res jobResult) {
		if answered {
			return
		}

		answered = true
		work.result <- res
	}

	panicked := p.guard.Do(work.ctx, "worker.process_job", func() {
		pyroscope.TagWrapper(work.ctx, pyroscope.Labels("operation", "worker.process_job"), func(ctx context.Context) {
			plugin, err := p.inner.Get(ctx, work.pluginGroup, work.pluginName, work.pluginVersion)
			if err != nil {
				answer(jobResult{err: err})

				return
			}

			wrapped := &poolPlugin{
				inner:   plugin,
				cfg:     p.cfg,
				logger:  p.logger,
				metrics: p.metrics,
				gen:     p.gen,
			}

			answer(jobResult{plugin: wrapped})
		})
	})

	// Without this the caller waits out its whole request deadline for a reply
	// that is never coming, so a panic would present as a timeout rather than
	// as the failure it is.
	if panicked {
		answer(jobResult{err: ErrGenerationFailed})
	}
}

// Get создаёт задание и отправляет его в очередь с backpressure.
func (p *WorkerPool) Get(ctx context.Context, pluginGroup, pluginName, pluginVersion string) (Plugin, error) {
	ctx, span := p.tracer.Start(ctx, "pool.Get",
		trace.WithAttributes(
			attribute.String("plugin.group", pluginGroup),
			attribute.String("plugin.name", pluginName),
			attribute.String("plugin.version", pluginVersion),
		))
	defer span.End()

	if p.closed.Load() {
		return nil, ErrShuttingDown
	}

	resultCh := make(chan jobResult, 1)
	jobItem := job{
		ctx:           ctx,
		pluginGroup:   pluginGroup,
		pluginName:    pluginName,
		pluginVersion: pluginVersion,
		result:        resultCh,
	}

	select {
	case p.jobs <- jobItem:
		// Job принят в очередь
		p.jobsTotal.Inc()
	default:
		p.logger.Warn("job queue full, rejecting request")
		p.rejectedTotal.Inc()
		span.AddEvent("pool.rejected")

		return nil, ErrServerOverloaded
	}

	queueStart := time.Now()

	select {
	case res := <-resultCh:
		span.SetAttributes(attribute.Int64("pool.queue_wait_ms", time.Since(queueStart).Milliseconds()))

		return res.plugin, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// List проксирует запрос напрямую во внутренний Registry без очереди.
func (p *WorkerPool) List(ctx context.Context, filter PluginFilter) ([]PluginInfo, error) {
	return p.inner.List(ctx, filter)
}

// Create проксирует запрос напрямую во внутренний Registry без очереди.
func (p *WorkerPool) Create(ctx context.Context, req CreatePluginRequest) (*PluginInfo, error) {
	return p.inner.Create(ctx, req)
}

// Update проксирует запрос напрямую во внутренний Registry без очереди.
func (p *WorkerPool) Update(ctx context.Context, req UpdatePluginRequest) (*PluginInfo, error) {
	return p.inner.Update(ctx, req)
}

// Delete проксирует запрос напрямую во внутренний Registry без очереди.
func (p *WorkerPool) Delete(ctx context.Context, group, name, version string) error {
	return p.inner.Delete(ctx, group, name, version)
}

// Generate выполняет генерацию кода с таймаутом и retry.
func (pp *poolPlugin) Generate(ctx context.Context, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	// The slot is taken before the deadline starts. Time spent queueing is not
	// the plugin's, and charging it against GenerationTimeout would fail
	// requests that never got the chance to run.
	release, err := pp.gen.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	genCtx, cancel := context.WithTimeout(ctx, pp.cfg.GenerationTimeout)
	defer cancel()

	info := pp.inner.Info(ctx)
	pluginName := info.Group + "/" + info.Name + ":" + info.Version

	start := time.Now()
	resp, err := pp.generateWithRetries(ctx, genCtx, req, pluginName)
	pp.metrics.ObserveGenerationDuration(ctx, pluginName, time.Since(start))

	// A caller that walked away is not a plugin failure, so it is not counted
	// as one. A deadline hit by genCtx alone leaves ctx healthy and is counted.
	if err != nil && ctx.Err() == nil {
		errorType := "permanent"
		if isTransient(err) {
			errorType = "transient"
		}
		pp.metrics.IncGenerationErrors(ctx, pluginName, errorType)
	}

	return resp, err
}

// generateWithRetries выполняет генерацию, повторяя попытки при временных
// ошибках. genCtx несёт таймаут генерации, ctx — жизнь вызывающего.
func (pp *poolPlugin) generateWithRetries( //nolint:funcorder // деталь реализации poolPlugin
	ctx, genCtx context.Context,
	req *pluginpb.CodeGeneratorRequest,
	pluginName string,
) (*pluginpb.CodeGeneratorResponse, error) {
	var resp *pluginpb.CodeGeneratorResponse
	var err error

	maxAttempts := pp.cfg.MaxRetries + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return nil, ctxErr
		}

		resp, err = pp.inner.Generate(genCtx, req)
		if err == nil {
			return resp, nil
		}

		if !isTransient(err) || attempt == maxAttempts {
			break
		}

		pp.metrics.IncGenerationRetries(ctx, pluginName)

		pp.logger.Warn("retrying generation",
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"error", err,
		)
	}

	return resp, err
}

// Info проксирует запрос во внутренний Plugin.
func (pp *poolPlugin) Info(ctx context.Context) *PluginInfo {
	return pp.inner.Info(ctx)
}

// isTransient определяет, является ли ошибка временной.
func isTransient(err error) bool {
	if err == nil {
		return false
	}

	// context.DeadlineExceeded НЕ транзиентная
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Подстроки, указывающие на транзиентные ошибки
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return true
	case strings.Contains(msg, "temporary failure"):
		return true
	}

	return false
}

// Shutdown закрывает канал заданий и ожидает завершения воркеров.
// Возвращает количество потерянных заданий.
func (p *WorkerPool) Shutdown(timeout time.Duration) int {
	p.closed.Store(true)
	close(p.jobs)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return 0
	case <-time.After(timeout):
		lost := len(p.jobs)
		p.logger.Warn("shutdown timeout, jobs lost", "count", lost)

		return lost
	}
}
