package metrics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const defaultQueryTimeout = 5 * time.Second

// Compile-time check that BusinessMetricsCollector implements prometheus.Collector.
var _ prometheus.Collector = (*BusinessMetricsCollector)(nil)

// BusinessMetricsCollector collects business metrics from PostgreSQL.
// It implements the prometheus.Collector interface.
type BusinessMetricsCollector struct {
	db  *sql.DB
	log *slog.Logger

	pluginsTotal        *prometheus.Desc
	pluginsByGroup      *prometheus.Desc
	auditLogTotal       *prometheus.Desc
	pluginVersionsCount *prometheus.Desc
}

// NewBusinessMetricsCollector creates a new BusinessMetricsCollector.
func NewBusinessMetricsCollector(db *sql.DB, namespace string, log *slog.Logger) *BusinessMetricsCollector {
	return &BusinessMetricsCollector{
		db:  db,
		log: log,
		pluginsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "business", "plugins_total"),
			"Total number of registered plugins.",
			nil, nil,
		),
		pluginsByGroup: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "business", "plugins_by_group"),
			"Number of plugins per group.",
			[]string{"group"}, nil,
		),
		auditLogTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "business", "audit_log_total"),
			"Approximate number of audit log entries, from the planner's row estimate. Lags bulk changes until autovacuum runs.",
			nil, nil,
		),
		pluginVersionsCount: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "business", "plugin_versions_count"),
			"Number of unique versions per plugin.",
			[]string{"group", "name"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *BusinessMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pluginsTotal
	ch <- c.pluginsByGroup
	ch <- c.auditLogTotal
	ch <- c.pluginVersionsCount
}

// auditRowEstimate reads the planner's row estimate for every partition of
// audit_log instead of counting the rows.
//
// count(*) on this table is a sequential scan of the whole retention window, and
// the collector runs it on every scrape. Measured on real data: 58 ms at 4M rows,
// 1245 ms at 16M, growing linearly, against a 5-second query timeout and a
// 30-second scrape interval — so on a year of history the metric would quietly
// start timing out, blanking the dashboards exactly when the service is busiest.
// The estimate answers the same question in 4 ms.
//
// It is an estimate, and it lags. Once ANALYZE has run it is accurate to a
// handful of rows in sixteen million, but immediately after a bulk load it can
// be out by a factor of several until autovacuum catches up — observed at 2.4M
// against a true 16M. That is fine for what this metric is for: whether
// retention is working and how fast the table is growing, on a twelve-month
// window. Do not build anything on it that needs the exact number.
//
// The sum runs over pg_inherits: audit_log is partitioned, so its parent holds
// no rows and reltuples on the parent alone reports zero.
const auditRowEstimate = `
SELECT coalesce(sum(GREATEST(c.reltuples, 0)), 0)::bigint
FROM pg_class c
JOIN pg_inherits i ON i.inhrelid = c.oid
WHERE i.inhparent = 'audit_log'::regclass`

// Collect implements prometheus.Collector.
//
// Everything here runs synchronously on every scrape, so nothing may grow with
// the amount of history. What is left describes *state* — how many plugins exist
// — which only the database knows: a gauge kept in memory would start at zero
// after a restart and never learn the truth until someone happened to create a
// plugin, and with several replicas each would answer for itself.
//
// Everything that described *events* has moved out. Counts by operation and by
// status are now easyp_operations_total, incremented where the events happen;
// activity over a window is increase() over that counter, computed by Prometheus;
// and the default-partition tripwire moved to the six-hourly partition
// maintenance, where its cadence belongs. Between them those three were 2.3
// seconds of sequential scanning on every scrape at sixteen million audit rows,
// and they grew from there.
func (c *BusinessMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	// Scalar metrics
	c.collectScalar(ch, c.pluginsTotal, "plugins_total",
		"SELECT count(*) FROM plugins")
	c.collectScalar(ch, c.auditLogTotal, "audit_log_total", auditRowEstimate)

	// Grouped metrics
	c.collectGrouped(ch, c.pluginsByGroup, "plugins_by_group",
		"SELECT group_name, count(*) FROM plugins GROUP BY group_name")
	c.collectGrouped2(ch, c.pluginVersionsCount, "plugin_versions_count",
		"SELECT group_name, name, count(DISTINCT version) FROM plugins GROUP BY group_name, name")
}

func (c *BusinessMetricsCollector) collectScalar(ch chan<- prometheus.Metric, desc *prometheus.Desc, name, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultQueryTimeout)
	defer cancel()

	var count int64
	err := c.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		c.log.Error("failed to collect metric", "metric", name, "error", err)

		return
	}

	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(count))
}

func (c *BusinessMetricsCollector) collectGrouped(ch chan<- prometheus.Metric, desc *prometheus.Desc, name, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultQueryTimeout)
	defer cancel()

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		c.log.Error("failed to collect metric", "metric", name, "error", err)

		return
	}
	defer rows.Close() //nolint:errcheck // rows.Close() error is secondary to rows.Err()

	for rows.Next() {
		var labelValue string
		var count int64
		err := rows.Scan(&labelValue, &count)
		if err != nil {
			c.log.Error("failed to collect metric", "metric", name, "error", err)

			return
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(count), labelValue)
	}

	err = rows.Err()
	if err != nil {
		c.log.Error("failed to collect metric", "metric", name, "error", err)
	}
}

func (c *BusinessMetricsCollector) collectGrouped2(ch chan<- prometheus.Metric, desc *prometheus.Desc, name, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultQueryTimeout)
	defer cancel()

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		c.log.Error("failed to collect metric", "metric", name, "error", err)

		return
	}
	defer rows.Close() //nolint:errcheck // rows.Close() error is secondary to rows.Err()

	for rows.Next() {
		var label1, label2 string
		var count int64
		err := rows.Scan(&label1, &label2, &count)
		if err != nil {
			c.log.Error("failed to collect metric", "metric", name, "error", err)

			return
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(count), label1, label2)
	}

	err = rows.Err()
	if err != nil {
		c.log.Error("failed to collect metric", "metric", name, "error", err)
	}
}
