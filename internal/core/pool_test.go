package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/pluginpb"
)

// blockingPlugin records how many generations run at once and holds each one
// until release is closed, so a test can observe the peak.
type blockingPlugin struct {
	release chan struct{}
	running atomic.Int64
	peak    atomic.Int64
	started chan struct{}
}

func newBlockingPlugin(expected int) *blockingPlugin {
	return &blockingPlugin{
		release: make(chan struct{}),
		started: make(chan struct{}, expected),
	}
}

func (p *blockingPlugin) Generate(
	ctx context.Context,
	_ *pluginpb.CodeGeneratorRequest,
) (*pluginpb.CodeGeneratorResponse, error) {
	now := p.running.Add(1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			break
		}
	}

	defer p.running.Add(-1)

	select {
	case p.started <- struct{}{}:
	default:
	}

	select {
	case <-p.release:
		return &pluginpb.CodeGeneratorResponse{}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("blockingPlugin: %w", ctx.Err())
	}
}

func (p *blockingPlugin) Info(_ context.Context) *PluginInfo {
	return &PluginInfo{Group: "test", Name: "plugin", Version: "v1.0.0"}
}

// staticRegistry hands out the same plugin to every caller.
type staticRegistry struct{ plugin Plugin }

func (r *staticRegistry) Get(_ context.Context, _, _, _ string) (Plugin, error) {
	return r.plugin, nil
}

func (r *staticRegistry) List(_ context.Context, _ PluginFilter) ([]PluginInfo, error) {
	return nil, nil
}

func (r *staticRegistry) Create(_ context.Context, _ CreatePluginRequest) (*PluginInfo, error) {
	return nil, nil
}

func (r *staticRegistry) Update(_ context.Context, _ UpdatePluginRequest) (*PluginInfo, error) {
	return nil, nil
}

func (r *staticRegistry) Delete(_ context.Context, _, _, _ string) error { return nil }

// noopMetrics satisfies Metrics without recording anything.
type noopMetrics struct{}

func (noopMetrics) GenerateCode(_ context.Context, _ PluginInfo) error { return nil }

func (noopMetrics) ObserveGenerationDuration(_ context.Context, _ string, _ time.Duration) {}

func (noopMetrics) IncGenerationErrors(_ context.Context, _, _ string) {}

func (noopMetrics) IncGenerationRetries(_ context.Context, _ string) {}
func (noopMetrics) IncOperation(_ context.Context, _, _ string)      {}

func newTestPool(t *testing.T, plugin Plugin, cfg WorkerPoolConfig) *WorkerPool {
	t.Helper()

	pool := NewWorkerPool(
		&staticRegistry{plugin: plugin},
		cfg,
		slog.New(slog.DiscardHandler),
		noopMetrics{},
		nil,
		"test",
	)
	pool.Start(t.Context())

	return pool
}

// TestGenerationConcurrencyIsBounded pins the property the generation limiter
// exists for. Before it was added the pool bounded only plugin lookups: a
// worker returned the plugin and moved on, and Generate ran on the caller's
// goroutine with no limit at all. This test fails without the limiter, with a
// peak equal to the number of callers.
func TestGenerationConcurrencyIsBounded(t *testing.T) {
	t.Parallel()

	const (
		limit   = 3
		callers = 12
	)

	plugin := newBlockingPlugin(callers)
	pool := newTestPool(t, plugin, WorkerPoolConfig{
		Workers:                  4,
		QueueSize:                callers,
		MaxConcurrentGenerations: limit,
		GenerationTimeout:        10 * time.Second,
	})

	var wg sync.WaitGroup

	for range callers {
		wg.Go(func() {
			p, err := pool.Get(t.Context(), "test", "plugin", "v1.0.0")
			if err != nil {
				return
			}

			_, _ = p.Generate(t.Context(), &pluginpb.CodeGeneratorRequest{})
		})
	}

	// Wait until the limiter is saturated, then let everyone finish.
	for range limit {
		select {
		case <-plugin.started:
		case <-time.After(5 * time.Second):
			t.Fatal("generations did not start")
		}
	}

	assert.LessOrEqual(t, plugin.peak.Load(), int64(limit),
		"more plugin processes ran at once than the configured limit")

	close(plugin.release)
	wg.Wait()

	assert.LessOrEqual(t, plugin.peak.Load(), int64(limit))
}

// TestGenerationRejectsWhenQueueFull checks the refusal path: once the limiter
// is saturated and the wait queue is full, further callers are told so instead
// of piling up.
func TestGenerationRejectsWhenQueueFull(t *testing.T) {
	t.Parallel()

	const (
		limit = 1
		queue = 1
	)

	plugin := newBlockingPlugin(limit)
	pool := newTestPool(t, plugin, WorkerPoolConfig{
		Workers:                  2,
		QueueSize:                queue,
		MaxConcurrentGenerations: limit,
		GenerationTimeout:        10 * time.Second,
	})

	occupy, err := pool.Get(t.Context(), "test", "plugin", "v1.0.0")
	require.NoError(t, err)

	go func() { _, _ = occupy.Generate(t.Context(), &pluginpb.CodeGeneratorRequest{}) }()

	select {
	case <-plugin.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first generation did not start")
	}

	// One caller may wait; the next must be refused rather than queued.
	waiter, err := pool.Get(t.Context(), "test", "plugin", "v1.0.0")
	require.NoError(t, err)

	go func() { _, _ = waiter.Generate(t.Context(), &pluginpb.CodeGeneratorRequest{}) }()

	require.Eventually(t, func() bool {
		return pool.gen.waiting.Load() == int64(queue)
	}, 5*time.Second, 10*time.Millisecond, "queue never filled")

	rejected, err := pool.Get(t.Context(), "test", "plugin", "v1.0.0")
	require.NoError(t, err)

	_, err = rejected.Generate(t.Context(), &pluginpb.CodeGeneratorRequest{})
	require.ErrorIs(t, err, ErrServerOverloaded)

	close(plugin.release)
}
