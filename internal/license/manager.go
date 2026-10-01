package license

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/safe"
)

const defaultCacheTTL = 5 * time.Minute

// Config holds license manager configuration.
type Config struct {
	// CacheTTL is the interval between license refresh calls.
	// Defaults to 5 minutes when zero or negative.
	CacheTTL time.Duration `env:"LICENSE_CACHE_TTL" yaml:"cache_ttl"`
}

// Manager fetches, caches, and periodically refreshes license claims.
type Manager struct {
	mu      sync.RWMutex
	claims  core.LicenseClaims
	client  core.LicenseClient
	cfg     Config
	logger  *slog.Logger
	metrics *Metrics
	guard   *safe.Guard

	// startup is what the first fetch returned, nil until it has. The worker
	// pool reads its ceilings once, right after that fetch, and never again; a
	// later tier change moves every other limit at once but leaves the pool
	// where it started until the process restarts. Kept so refresh can say so.
	startup *core.LicenseClaims
}

// NewManager creates a Manager backed by the given LicenseClient.
// It performs an initial license fetch; on failure it falls back to community mode.
func NewManager(
	ctx context.Context,
	client core.LicenseClient,
	cfg Config,
	logger *slog.Logger,
	reg *prometheus.Registry,
	namespace string,
) (*Manager, error) {
	if client == nil {
		return nil, ErrNoClient
	}

	metrics := NewMetrics(reg, namespace)

	lm := &Manager{
		claims:  core.CommunityLicenseClaims(),
		client:  client,
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
		guard:   safe.NewGuard(reg, namespace),
	}

	if lm.cfg.CacheTTL <= 0 {
		lm.cfg.CacheTTL = defaultCacheTTL
	}

	// Perform initial fetch so claims are populated before the first request.
	lm.refresh(ctx)

	startup := lm.Claims()
	lm.startup = &startup

	return lm, nil
}

// Claims returns the cached LicenseClaims. Safe for concurrent use.
func (lm *Manager) Claims() core.LicenseClaims {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	return lm.claims
}

// StartRefreshWatcher starts a background goroutine that refreshes license claims
// on every CacheTTL tick. It stops when ctx is cancelled.
func (lm *Manager) StartRefreshWatcher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(lm.cfg.CacheTTL)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Вокруг одного обновления, а не вокруг цикла: паника снаружи
				// оставила бы процесс живым и навсегда с устаревшей лицензией —
				// то есть после истечения срока установка молча свалилась бы в
				// community, а метрика срока замерла бы на старом значении.
				lm.guard.Do(ctx, "license.refresh", func() { lm.refresh(ctx) })
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Metrics returns the Prometheus metrics collector (used by FeatureGate).
func (lm *Manager) Metrics() *Metrics {
	return lm.metrics
}

// refresh calls ValidateLicense and updates the cached claims.
// On error it keeps the previous claims (graceful fallback).
func (lm *Manager) refresh(ctx context.Context) {
	claims, err := lm.client.ValidateLicense(ctx)
	if err != nil {
		// Deliberately does not touch the gauges: the previous claims are still
		// what the service is enforcing, so the metrics should keep describing
		// them rather than report a state nothing is acting on.
		lm.logger.Warn("failed to validate license, keeping previous claims", "error", err)

		return
	}

	lm.mu.Lock()
	changed := lm.claims.Tier != claims.Tier ||
		lm.claims.MaxWorkers != claims.MaxWorkers ||
		lm.claims.MaxPlugins != claims.MaxPlugins ||
		lm.claims.MaxGenerations != claims.MaxGenerations ||
		len(lm.claims.Features) != len(claims.Features)
	lm.claims = claims
	lm.mu.Unlock()

	// Once every CacheTTL, indefinitely. At Info this buries the one event worth
	// seeing — the moment the tier actually changes — under identical lines.
	level := slog.LevelDebug
	if changed {
		level = slog.LevelInfo
	}

	lm.logger.Log(ctx, level, "license refreshed",
		"tier", claims.Tier,
		"max_workers", claims.MaxWorkers,
		"max_plugins", claims.MaxPlugins,
		"max_generations", claims.MaxGenerations,
		"features_count", len(claims.Features),
	)

	if changed {
		lm.warnPoolKeepsStartupCeilings(ctx, claims)
	}

	lm.metrics.observe(claims)
}

// warnPoolKeepsStartupCeilings reports a tier change the worker pool will not
// follow until restart.
//
// The case that matters is a licence lapsing past its grace period: audit and
// the plugin cap switch to community on this refresh, while the pool keeps
// running with the capacity it was started with. Without this line the drop
// arrives silently with the next deploy, weeks after the licence that caused it.
func (lm *Manager) warnPoolKeepsStartupCeilings(ctx context.Context, claims core.LicenseClaims) {
	if lm.startup == nil {
		return
	}

	if claims.MaxWorkers == lm.startup.MaxWorkers && claims.MaxGenerations == lm.startup.MaxGenerations {
		return
	}

	lm.logger.WarnContext(ctx,
		"licence tier changed while running; the worker pool keeps the ceilings it started with "+
			"and applies the new ones on restart",
		"tier", claims.Tier,
		"startup_tier", lm.startup.Tier,
		"max_workers", claims.MaxWorkers,
		"startup_max_workers", lm.startup.MaxWorkers,
		"max_generations", claims.MaxGenerations,
		"startup_max_generations", lm.startup.MaxGenerations,
	)
}
