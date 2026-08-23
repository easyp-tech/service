package core_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/core"
)

// flakyRegistry panics on the first n lookups and succeeds afterwards, so a
// test can ask what the pool does *after* a panic rather than only during one.
type flakyRegistry struct {
	core.Registry

	panicsLeft atomic.Int64
	calls      atomic.Int64
}

func (r *flakyRegistry) Get(context.Context, string, string, string) (core.Plugin, error) {
	r.calls.Add(1)

	if r.panicsLeft.Add(-1) >= 0 {
		panic("malformed plugin archive")
	}

	return stubPlugin{}, nil
}

type stubPlugin struct{}

func (stubPlugin) Generate(context.Context, *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	return &pluginpb.CodeGeneratorResponse{}, nil
}

func (stubPlugin) Info(context.Context) *core.PluginInfo {
	return &core.PluginInfo{Group: "g", Name: "n", Version: "v1.0.0"}
}

type noMetrics struct{}

func (noMetrics) GenerateCode(context.Context, core.PluginInfo) error              { return nil }
func (noMetrics) ObserveGenerationDuration(context.Context, string, time.Duration) {}
func (noMetrics) IncGenerationErrors(context.Context, string, string)              {}
func (noMetrics) IncGenerationRetries(context.Context, string)                     {}
func (noMetrics) IncOperation(context.Context, string, string)                     {}

func newPanicPool(t *testing.T, reg *prometheus.Registry, inner core.Registry) *core.WorkerPool {
	t.Helper()

	pool := core.NewWorkerPool(inner, core.WorkerPoolConfig{
		Workers:                  1,
		QueueSize:                4,
		MaxConcurrentGenerations: 1,
		GenerationTimeout:        5 * time.Second,
		MaxRetries:               0,
	}, slog.New(slog.DiscardHandler), noMetrics{}, reg, "easyp")

	pool.Start(t.Context())
	t.Cleanup(func() { pool.Shutdown(time.Second) })

	return pool
}

// Before the barrier existed this did not fail — it took the test binary down,
// which is precisely what it does to the service.
func TestPanicInLookupDoesNotKillTheProcess(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	inner := &flakyRegistry{}
	inner.panicsLeft.Store(1)

	pool := newPanicPool(t, reg, inner)

	_, err := pool.Get(t.Context(), "g", "n", "v1.0.0")

	require.Error(t, err, "a panicking lookup must surface as an error, not as a lost reply")
	require.ErrorIs(t, err, core.ErrGenerationFailed)
}

// The test that distinguishes a barrier around the job from one around the
// worker. With the latter the pool has no workers left by now and this call
// hangs until the context expires; the panic was still "handled".
func TestPoolKeepsWorkingAfterAPanic(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	inner := &flakyRegistry{}
	inner.panicsLeft.Store(1)

	pool := newPanicPool(t, reg, inner)

	_, err := pool.Get(t.Context(), "g", "n", "v1.0.0")
	require.Error(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	plugin, err := pool.Get(ctx, "g", "n", "v1.0.0")
	require.NoError(t, err, "the pool stopped serving after a panic; the worker did not survive")
	require.NotNil(t, plugin)
	require.Equal(t, int64(2), inner.calls.Load())
}

// A panic that repeats must not exhaust the pool either — one worker, several
// bad lookups, and it still answers the good one at the end.
func TestPoolSurvivesRepeatedPanics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	inner := &flakyRegistry{}
	inner.panicsLeft.Store(5)

	pool := newPanicPool(t, reg, inner)

	for range 5 {
		_, err := pool.Get(t.Context(), "g", "n", "v1.0.0")
		require.Error(t, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	_, err := pool.Get(ctx, "g", "n", "v1.0.0")
	require.NoError(t, err, "the pool ran out of workers after repeated panics")
	require.InDelta(t, 5, panicCount(t, reg), 0)
}

// Panics reach the metric the existing alert already reads, so background
// failures need no rule of their own — and there must be exactly one series,
// not one per package that reports into it.
func TestPanicsAreCounted(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	inner := &flakyRegistry{}
	inner.panicsLeft.Store(3)

	pool := newPanicPool(t, reg, inner)

	for range 3 {
		_, err := pool.Get(t.Context(), "g", "n", "v1.0.0")
		require.Error(t, err)
	}

	series, err := testutil.GatherAndCount(reg, "easyp_panics_total")
	require.NoError(t, err)
	require.Equal(t, 1, series)
	require.InDelta(t, 3, panicCount(t, reg), 0)
}

func panicCount(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != "easyp_panics_total" {
			continue
		}

		require.Len(t, f.GetMetric(), 1)

		return f.GetMetric()[0].GetCounter().GetValue()
	}

	t.Fatal("easyp_panics_total was never registered")

	return 0
}
