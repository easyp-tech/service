package ratelimiter

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/safe"
)

// clientBucket хранит rate.Limiter и время последнего обращения.
type clientBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	mu       sync.Mutex // защищает lastSeen
}

// RateLimiter реализует ratelimit.Limiter из grpc-ecosystem.
type RateLimiter struct {
	cfg          Config
	gate         core.FeatureGate
	keyExtractor KeyExtractor
	guard        *safe.Guard
	logger       *slog.Logger
	buckets      sync.Map // map[string]*clientBucket

	// Prometheus metrics
	requestsTotal *prometheus.CounterVec // labels: status (allowed/denied), client_ip
	activeClients prometheus.Gauge
}

// New создаёт RateLimiter и регистрирует метрики.
// keyExtractor определяет стратегию извлечения ключа. Если nil — используется PeerIPExtractor.
func New(
	cfg Config,
	gate core.FeatureGate,
	keyExtractor KeyExtractor,
	logger *slog.Logger,
	reg *prometheus.Registry,
	namespace string,
) *RateLimiter {
	if keyExtractor == nil {
		keyExtractor = PeerIPExtractor
	}

	if replaced := cfg.setDefault(); len(replaced) > 0 {
		logger.Warn("rate limiter settings were not usable and fell back to defaults",
			"fields", replaced)
	}

	// Deliberately not labelled by client IP: on a public listener that is an
	// unbounded label, and every address ever seen would become a permanent
	// time series. The address stays in the log line instead.
	requestsTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "rate_limit_requests_total",
			Help:      "Total number of requests processed by rate limiter",
		},
		[]string{"status"},
	)

	activeClients := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "rate_limit_active_clients",
			Help:      "Current number of active client buckets",
		},
	)

	if reg != nil {
		reg.MustRegister(requestsTotal)
		reg.MustRegister(activeClients)
	}

	return &RateLimiter{
		cfg:           cfg,
		gate:          gate,
		keyExtractor:  keyExtractor,
		guard:         safe.NewGuard(reg, namespace),
		logger:        logger,
		requestsTotal: requestsTotal,
		activeClients: activeClients,
	}
}

// Limit реализует ratelimit.Limiter.
// Возвращает nil если запрос разрешён, или status.Error(RESOURCE_EXHAUSTED) если отклонён.
func (rl *RateLimiter) Limit(ctx context.Context) error {
	// Step 1: Check FeatureGate — if nil (no license system), treat as enabled.
	if rl.gate != nil && !rl.gate.Enabled(core.FeatureRateLimiting) {
		return nil
	}

	// Step 2: Extract key via keyExtractor — empty string means fail-open.
	key := rl.keyExtractor(ctx)
	if key == "" {
		return nil
	}

	// Step 3: Get or create clientBucket for this key.
	newBucket := &clientBucket{
		limiter:  rate.NewLimiter(rate.Limit(rl.cfg.RequestsPerSecond), rl.cfg.Burst),
		lastSeen: time.Now(),
	}
	val, loaded := rl.buckets.LoadOrStore(key, newBucket)
	bucket, ok := val.(*clientBucket)
	if !ok {
		bucket = newBucket
	}

	if !loaded {
		// New bucket created — update active clients gauge.
		rl.activeClients.Inc()
	}

	// Step 4: Update lastSeen.
	bucket.mu.Lock()
	bucket.lastSeen = time.Now()
	bucket.mu.Unlock()

	// Step 5: Check token availability.
	allowed := bucket.limiter.Allow()

	// Step 6: Prepare rate limit headers.
	remaining := max(int(bucket.limiter.Tokens()), 0)

	tokensNeeded := float64(rl.cfg.Burst) - bucket.limiter.Tokens()
	if tokensNeeded < 0 {
		tokensNeeded = 0
	}
	resetDuration := time.Duration(tokensNeeded / rl.cfg.RequestsPerSecond * float64(time.Second))
	resetTime := time.Now().Add(resetDuration).Unix()

	md := metadata.Pairs(
		"x-ratelimit-limit", strconv.Itoa(rl.cfg.Burst),
		"x-ratelimit-remaining", strconv.Itoa(remaining),
		"x-ratelimit-reset", strconv.FormatInt(resetTime, 10),
	)

	// Step 7: Allowed — set headers, increment metric, return nil.
	if allowed {
		_ = grpc.SetHeader(ctx, md)
		rl.requestsTotal.WithLabelValues("allowed").Inc()

		return nil
	}

	// Step 8: Denied — set trailing metadata, increment metric, log, return error.
	_ = grpc.SetTrailer(ctx, md)
	rl.requestsTotal.WithLabelValues("denied").Inc()
	rl.logger.Warn("rate limit exceeded",
		slog.String("client_ip", key),
		slog.Int64("reset_time", resetTime),
	)

	return status.Errorf(codes.ResourceExhausted, "rate limit exceeded for %s, retry after %d", key, resetTime)
}

// StartCleanup запускает фоновую горутину очистки stale buckets.
// Останавливается при отмене контекста.
//
// Барьер стоит вокруг одной уборки, а не вокруг цикла: паника, пойманная
// снаружи, оставила бы процесс живым и навсегда без очистки, а вёдра растут по
// одному на каждый адрес и никогда не исчезают — то есть утечка памяти вместо
// падения.
func (rl *RateLimiter) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(rl.cfg.CleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.guard.Do(ctx, "ratelimiter.cleanup", rl.cleanup)
			}
		}
	}()
}

// cleanup удаляет stale buckets и обновляет метрику activeClients.
func (rl *RateLimiter) cleanup() {
	var active int
	rl.buckets.Range(func(key, value any) bool {
		bucket, ok := value.(*clientBucket)
		if !ok {
			return true
		}
		bucket.mu.Lock()
		lastSeen := bucket.lastSeen
		bucket.mu.Unlock()

		if time.Since(lastSeen) > rl.cfg.CleanupInterval {
			rl.buckets.Delete(key)
		} else {
			active++
		}

		return true
	})
	rl.activeClients.Set(float64(active))
}
