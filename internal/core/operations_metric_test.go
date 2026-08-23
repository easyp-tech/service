package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// listPluginsWith drives one failing ListPlugins through Core and returns what
// the metrics adapter was asked to count.
func listPluginsWith(t *testing.T, gate FeatureGate, sink AuditSink) []operationCount {
	t.Helper()

	metrics := &countingMetrics{}
	module := New(metrics, failingRegistry{}, gate, sink, testLogger())

	_, err := module.ListPlugins(t.Context(), PluginFilter{}, PluginPage{})
	require.Error(t, err)

	return metrics.recorded()
}

func TestOperationCountedOnError(t *testing.T) {
	t.Parallel()

	got := listPluginsWith(t, enterpriseGate(), &fakeSink{})

	require.Equal(t, []operationCount{{operation: OperationListPlugins, status: AuditStatusError}}, got)
}

func TestOperationCountedOnSuccess(t *testing.T) {
	t.Parallel()

	metrics := &countingMetrics{}
	module := New(metrics, emptyRegistry{}, enterpriseGate(), &fakeSink{}, testLogger())

	_, err := module.ListPlugins(t.Context(), PluginFilter{}, PluginPage{})
	require.NoError(t, err)

	require.Equal(t,
		[]operationCount{{operation: OperationListPlugins, status: AuditStatusSuccess}},
		metrics.recorded())
}

// The test this file exists for.
//
// The increment sits above both early returns in sendAudit on purpose: it counts
// what the service did, not what reached the audit log. Audit is an Enterprise
// feature, so putting the line below the gate would leave community
// installations with no error rate and no operation breakdown at all —
// observability of your own service is not something anyone should have to buy.
//
// Move the call below either early return and this reddens; move it below only
// one, and one of the two cases below still catches it.
func TestOperationCountedWhenAuditIsOff(t *testing.T) {
	t.Parallel()

	t.Run("community licence denies audit", func(t *testing.T) {
		t.Parallel()

		sink := &fakeSink{}
		got := listPluginsWith(t, communityGate(), sink)

		require.Equal(t,
			[]operationCount{{operation: OperationListPlugins, status: AuditStatusError}}, got,
			"the operation went uncounted because audit is not licensed")
		require.Equal(t, 1, sink.skippedCount(), "the audit entry should still be reported as skipped")
	})

	t.Run("no audit sink configured", func(t *testing.T) {
		t.Parallel()

		got := listPluginsWith(t, enterpriseGate(), nil)

		require.Equal(t,
			[]operationCount{{operation: OperationListPlugins, status: AuditStatusError}}, got,
			"the operation went uncounted because no audit sink is wired")
	})
}

// emptyRegistry succeeds with nothing, which drives Core down its success path.
type emptyRegistry struct{ Registry }

func (emptyRegistry) List(_ context.Context, _ PluginFilter, _ PluginPage) ([]PluginInfo, error) {
	return nil, nil
}
