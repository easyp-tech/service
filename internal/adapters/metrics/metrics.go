// Package metrics provides a metrics adapter for the EasyP plugin server.
package metrics

import (
	"context"

	"github.com/easyp-tech/service/internal/core"
)

var _ core.Metrics = NoMetrics{}

// NoMetrics is a nil metrics adapter for the EasyP plugin server.
type NoMetrics struct{}

// GenerateCode implements the core.Metrics interface.
func (NoMetrics) GenerateCode(ctx context.Context, pluginInfo string) error { return nil }
