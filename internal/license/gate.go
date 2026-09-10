package license

import (
	"slices"

	"github.com/easyp-tech/service/internal/core"
)

// FeatureGate checks feature availability against the current license.
type FeatureGate struct {
	manager *Manager
	metrics *Metrics
}

// NewFeatureGate creates a FeatureGate bound to the given Manager.
func NewFeatureGate(manager *Manager) *FeatureGate {
	return &FeatureGate{
		manager: manager,
		metrics: manager.Metrics(),
	}
}

// Enabled reports whether the given feature is permitted by the current license.
// Returns false for unrecognised Feature values.
func (fg *FeatureGate) Enabled(feat core.Feature) bool {
	// Step 1: Convert and validate.
	lf := feature(feat)
	if !lf.Valid() {
		return false
	}

	// Step 2: Get current claims (thread-safe).
	claims := fg.manager.Claims()

	// Step 3: Enterprise tier → all features enabled.
	if claims.Tier == core.LicenseTierEnterprise {
		return true
	}

	// Step 4: Enterprise-only feature in non-Enterprise mode → deny + metric.
	if lf.IsEnterprise() {
		fg.metrics.featureDenied.WithLabelValues(lf.String()).Inc()

		return false
	}

	// Step 5: Check if feature is in claims.Features.
	return slices.Contains(claims.Features, feat)
}

// MaxWorkers returns the worker concurrency limit from the current license.
func (fg *FeatureGate) MaxWorkers() int {
	return fg.manager.Claims().MaxWorkers
}

// MaxPlugins returns the maximum number of registered plugins. -1 means unlimited.
func (fg *FeatureGate) MaxPlugins() int {
	return fg.manager.Claims().MaxPlugins
}

// MaxGenerations returns the licence's ceiling on concurrent plugin processes.
func (fg *FeatureGate) MaxGenerations() int {
	return fg.manager.Claims().MaxGenerations
}
