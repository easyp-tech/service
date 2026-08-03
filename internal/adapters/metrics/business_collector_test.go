package metrics_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/adapters/metrics"
)

// describedNames returns the fully-qualified name of every metric the collector
// advertises.
func describedNames(t *testing.T) []string {
	t.Helper()

	c := metrics.NewBusinessMetricsCollector(nil, "easyp", slog.New(slog.DiscardHandler))

	ch := make(chan *prometheus.Desc, 32)
	c.Describe(ch)
	close(ch)

	var names []string

	for desc := range ch {
		// Desc has no accessor for its name; its String form embeds fqName.
		s := desc.String()
		start := strings.Index(s, "fqName: \"")
		require.GreaterOrEqual(t, start, 0, "unexpected Desc format: %s", s)

		rest := s[start+len("fqName: \""):]
		end := strings.Index(rest, "\"")
		require.GreaterOrEqual(t, end, 0, "unexpected Desc format: %s", s)

		names = append(names, rest[:end])
	}

	return names
}

// The collector runs synchronously on every scrape, so its surface is a
// statement about what the database is asked for at scrape cadence. Three
// metrics were removed from it because each meant a sequential scan of the whole
// audit history — together 2.3 seconds at sixteen million rows, growing from
// there. They now come from easyp_operations_total, from increase() over it, and
// from the six-hourly partition maintenance respectively.
//
// This pins the surface, not the SQL: re-adding one of them means re-adding its
// Desc, which reddens here.
func TestCollectorSurfaceExcludesAuditScans(t *testing.T) {
	t.Parallel()

	names := describedNames(t)

	require.ElementsMatch(t, []string{
		"easyp_business_plugins_total",
		"easyp_business_plugins_by_group",
		"easyp_business_plugin_versions_count",
		"easyp_business_audit_log_total",
	}, names)
}

// Named individually so a failure says which one came back and why it should
// not have.
func TestRemovedMetricsStayRemoved(t *testing.T) {
	t.Parallel()

	names := describedNames(t)

	for _, gone := range []struct {
		name  string
		place string
	}{
		{"easyp_business_audit_log_by_operation", "easyp_operations_total"},
		{"easyp_business_audit_log_by_status", "easyp_operations_total"},
		{"easyp_business_audit_log_last_24h", "increase(easyp_operations_total[24h])"},
		{"easyp_business_audit_log_default_rows", "easyp_audit_default_partition_used"},
	} {
		require.NotContains(t, names, gone.name,
			"%s scans the whole audit table on every scrape; it lives in %s now", gone.name, gone.place)
	}
}
