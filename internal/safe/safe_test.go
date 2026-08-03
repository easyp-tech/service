package safe_test

import (
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/safe"
)

func TestDoReportsNoPanicForOrdinaryWork(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	g := safe.NewGuard(reg, "test")

	ran := false
	panicked := g.Do(t.Context(), "work", func() { ran = true })

	require.True(t, ran)
	require.False(t, panicked)
	require.InDelta(t, 0, testutil.ToFloat64(g.Counter()), 0)
}

func TestDoRecoversAndCounts(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	g := safe.NewGuard(reg, "test")

	panicked := g.Do(t.Context(), "work", func() { panic("boom") })

	require.True(t, panicked)
	require.InDelta(t, 1, testutil.ToFloat64(g.Counter()), 0)
}

// The property the whole package exists for: the caller keeps going. A barrier
// that catches the panic but stops the work is not an improvement on crashing —
// it is the same outage with no stack trace.
func TestDoLetsTheCallerContinue(t *testing.T) {
	t.Parallel()

	g := safe.NewGuard(prometheus.NewRegistry(), "test")

	completed := 0

	for i := range 5 {
		g.Do(t.Context(), "work", func() {
			if i == 2 {
				panic("boom")
			}

			completed++
		})
	}

	require.Equal(t, 4, completed, "work stopped at the panic instead of continuing past it")
	require.InDelta(t, 1, testutil.ToFloat64(g.Counter()), 0)
}

// A panicking recover handler would itself be an unrecovered panic. Nil panic
// values and typed values both have to survive formatting.
func TestDoHandlesAnyPanicValue(t *testing.T) {
	t.Parallel()

	g := safe.NewGuard(prometheus.NewRegistry(), "test")

	require.True(t, g.Do(t.Context(), "err", func() { panic(require.New) }))
	require.True(t, g.Do(t.Context(), "int", func() { panic(42) }))
	require.True(t, g.Do(t.Context(), "struct", func() { panic(struct{ A int }{1}) }))
}

// Every package builds its own Guard from the registry it already holds. If
// that produced a second series, half the panics would be invisible to an alert
// reading the other one.
func TestGuardsShareOneCounter(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	first := safe.NewGuard(reg, "easyp")
	second := safe.NewGuard(reg, "easyp")

	first.Do(t.Context(), "a", func() { panic("boom") })
	second.Do(t.Context(), "b", func() { panic("boom") })

	require.InDelta(t, 2, testutil.ToFloat64(first.Counter()), 0)
	require.InDelta(t, 2, testutil.ToFloat64(second.Counter()), 0,
		"the second guard registered a series of its own")

	count, err := testutil.GatherAndCount(reg, "easyp_panics_total")
	require.NoError(t, err)
	require.Equal(t, 1, count, "more than one easyp_panics_total series exists")
}

func TestGoRecovers(t *testing.T) {
	t.Parallel()

	g := safe.NewGuard(prometheus.NewRegistry(), "test")

	var wg sync.WaitGroup

	wg.Add(1)

	g.Go(t.Context(), "once", func() {
		defer wg.Done()
		panic("boom")
	})

	wg.Wait()

	require.InDelta(t, 1, testutil.ToFloat64(g.Counter()), 0)
}

func TestNilRegistryIsUsable(t *testing.T) {
	t.Parallel()

	g := safe.NewGuard(nil, "test")

	require.True(t, g.Do(t.Context(), "work", func() { panic("boom") }))
	require.InDelta(t, 1, testutil.ToFloat64(g.Counter()), 0)
}

// Constructors here take reg as *prometheus.Registry and pass it straight
// through, and tests routinely pass a nil one. If NewGuard ever takes the
// Registerer interface instead, that nil pointer arrives wrapped in a non-nil
// interface, the nil check silently stops matching, and every such constructor
// panics on a nil dereference — which is how this was first written.
func TestTypedNilRegistryDoesNotPanic(t *testing.T) {
	t.Parallel()

	var reg *prometheus.Registry

	require.NotPanics(t, func() {
		g := safe.NewGuard(reg, "test")
		g.Do(t.Context(), "work", func() {})
	})
}
