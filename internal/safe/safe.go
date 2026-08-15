// Package safe contains the panic barrier for work that runs outside a gRPC
// handler.
//
// The gRPC recovery interceptor only protects the goroutine the handler runs
// on. Everything else the service does — locating a plugin in the worker pool,
// draining the audit queue, refreshing the licence, scanning the plugin cache —
// happens on goroutines of its own, where an unrecovered panic ends the whole
// process rather than the one request that caused it. For a service whose
// working day is spent executing third-party binaries, that is one malformed
// archive away.
package safe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/monitor"
)

// Guard recovers panics and counts them.
//
// Recover around the unit of work, not around the loop that repeats it. A
// barrier wrapping a whole worker or ticker leaves the process alive and the
// work permanently stopped: no plugins located, no audit written, no licence
// refreshed. That is harder to diagnose than the crash it replaced, because
// nothing about it looks like a failure. Use Do inside the loop body and keep
// Go for goroutines that genuinely run once.
type Guard struct {
	panics prometheus.Counter
}

// NewGuard returns a Guard reporting into namespace_panics_total on reg.
//
// Registering a counter that is already there returns it rather than failing,
// so every package can call this with the registry and namespace its
// constructor already receives and they all share one series. The alternative —
// threading one counter through six constructors — spreads a cross-cutting
// concern across signatures that have no other reason to know about it.
//
// Sharing the series matters beyond tidiness: the gRPC interceptor already
// reports here, and the PrometheusRule already alerts on it. Background panics
// join an alert that exists instead of needing one of their own.
//
// A nil reg yields a working Guard with an unregistered counter, which is what
// tests want.
//
// The parameter is the concrete *prometheus.Registry rather than the
// Registerer interface, matching every constructor in this codebase. It has to
// be: callers hold a *prometheus.Registry that is often nil in tests, and a nil
// pointer placed in an interface is not a nil interface — the check below would
// pass and Register would dereference nothing. Taking the concrete type makes
// the comparison mean what it says.
func NewGuard(reg *prometheus.Registry, namespace string) *Guard {
	counter := prometheus.NewCounter(prometheus.CounterOpts{ //nolint:exhaustruct // Namespace and Name identify it.
		Namespace: namespace,
		Name:      "panics_total",
		Help:      "Total number of panics recovered, in gRPC handlers and in background goroutines.",
	})

	if reg == nil {
		return &Guard{panics: counter}
	}

	err := reg.Register(counter)
	if err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(prometheus.Counter); ok {
				return &Guard{panics: existing}
			}
		}

		// Anything else is a genuine registration conflict — a different
		// collector under the same name — and hiding it would leave panics
		// uncounted with nothing to say so.
		panic(fmt.Sprintf("safe.NewGuard: registering %s_panics_total: %v", namespace, err))
	}

	return &Guard{panics: counter}
}

// Counter exposes the shared panic counter, for callers that have to hand it to
// something expecting a plain prometheus.Counter.
func (g *Guard) Counter() prometheus.Counter { //nolint:ireturn // The metric interface is the type.
	return g.panics
}

// Do runs fn, recovering and reporting a panic. It returns true if fn panicked,
// so a caller can turn that into an error for whoever is waiting on the result.
//
// Call this around one unit of work — one job, one flush, one tick — so that the
// loop around it survives to attempt the next.
//
// The named return is load-bearing rather than stylistic: the recover below
// runs in a deferred closure, and only a named result can be assigned from
// there. Made a plain local, it would be discarded and Do would report every
// panic it caught as a clean run.
//
//nolint:nonamedreturns // the deferred recover assigns to it; see above
func (g *Guard) Do(ctx context.Context, name string, fn func()) (panicked bool) {
	defer func() {
		reason := recover()
		if reason == nil {
			return
		}

		panicked = true
		g.panics.Inc()

		monitor.FromContext(ctx).Error("recovered panic in background work",
			slog.String("goroutine", name),
			slog.Any("panic_reason", reason),
			slog.String("stacktrace", string(debug.Stack())),
		)
	}()

	fn()

	return false
}

// Go runs fn on a new goroutine behind the same barrier. For goroutines that
// loop, prefer starting the goroutine plainly and calling Do inside the loop:
// a panic caught out here ends the loop for good.
func (g *Guard) Go(ctx context.Context, name string, fn func()) {
	go func() {
		g.Do(ctx, name, fn)
	}()
}
