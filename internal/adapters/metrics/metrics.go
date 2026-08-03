// Package metrics provides a metrics adapter for the EasyP plugin server.
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/core"
)

var _ core.Metrics = Metrics{}

// Metrics is the metrics adapter for the EasyP plugin server.
type Metrics struct {
	generated          *prometheus.CounterVec
	generationDuration *prometheus.HistogramVec
	generationErrors   *prometheus.CounterVec
	generationRetries  *prometheus.CounterVec
	operations         *prometheus.CounterVec
}

const labelPlugin = "plugin"

// New creates and returns a new Metrics adapter.
func New(reg *prometheus.Registry, namespace string) *Metrics {
	metrics := &Metrics{
		generated: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "generated_plugin_code_total",
				Help:      "Total number of generated code requests by plugin.",
			},
			[]string{labelPlugin},
		),
		generationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "generation_duration_seconds",
				Help:      "Duration of code generation in seconds.",
			},
			[]string{labelPlugin},
		),
		generationErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "generation_errors_total",
				Help:      "Total number of generation errors by plugin and error type.",
			},
			[]string{labelPlugin, "error_type"},
		),
		generationRetries: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "generation_retries_total",
				Help:      "Total number of generation retries by plugin.",
			},
			[]string{labelPlugin},
		),
		// A counter, not a gauge of rows in the audit table. The question worth
		// asking of it is a rate — how many operations per second, how many of
		// them failing — and a running total of table rows cannot answer that.
		//
		// Ten series at most: five operation types times two outcomes, all of
		// them constants in core. Nothing here comes from a request.
		operations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "operations_total",
				Help:      "Total operations performed, by type and outcome. Counted in every tier, audit or not.",
			},
			[]string{"operation", "status"},
		),
	}

	reg.MustRegister(metrics.generated)
	reg.MustRegister(metrics.generationDuration)
	reg.MustRegister(metrics.generationErrors)
	reg.MustRegister(metrics.generationRetries)
	reg.MustRegister(metrics.operations)

	return metrics
}

// GenerateCode implements the core.Metrics interface.
func (m Metrics) GenerateCode(_ context.Context, info core.PluginInfo) error {
	plugin := info.Group + "/" + info.Name + ":" + info.Version
	m.generated.WithLabelValues(plugin).Inc()

	return nil
}

// ObserveGenerationDuration records the duration of a code generation attempt.
func (m Metrics) ObserveGenerationDuration(_ context.Context, pluginName string, duration time.Duration) {
	m.generationDuration.WithLabelValues(pluginName).Observe(duration.Seconds())
}

// IncGenerationErrors increments the generation error counter.
func (m Metrics) IncGenerationErrors(_ context.Context, pluginName string, errorType string) {
	m.generationErrors.WithLabelValues(pluginName, errorType).Inc()
}

// IncOperation counts one completed operation by type and outcome.
func (m Metrics) IncOperation(_ context.Context, operation, status string) {
	m.operations.WithLabelValues(operation, status).Inc()
}

// IncGenerationRetries increments the generation retry counter.
func (m Metrics) IncGenerationRetries(_ context.Context, pluginName string) {
	m.generationRetries.WithLabelValues(pluginName).Inc()
}
