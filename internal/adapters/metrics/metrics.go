// Package metrics provides a metrics adapter for the EasyP plugin server.
package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/core"
)

var _ core.Metrics = NoMetrics{}

// NoMetrics is a nil metrics adapter for the EasyP plugin server.
type NoMetrics struct{}

// GenerateCode implements the core.Metrics interface.
func (NoMetrics) GenerateCode(context.Context, string) error { return nil }

// Metrics is the metrics adapter for the EasyP plugin server.
type Metrics struct {
	generated *prometheus.CounterVec
}

// New creates and returns a new Metrics adapter.
func New(reg *prometheus.Registry, namespace string) *Metrics {
	m := &Metrics{
		generated: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "generated_plugin_code_total",
				Help:      "Total number of generated code requests by plugin.",
			},
			[]string{"plugin"},
		),
	}

	reg.MustRegister(m.generated)

	return m
}

// GenerateCode implements the core.Metrics interface.
func (m Metrics) GenerateCode(ctx context.Context, plugin string) error {
	m.generated.WithLabelValues(plugin).Inc()
	return nil
}
