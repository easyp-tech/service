package ratelimiter

import (
	"context"
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/core"
)

// ConcurrencyLimiter bounds how many requests one client may have in flight.
//
// The rate limiter next door cannot do this: it implements ratelimit.Limiter
// from grpc-ecosystem, which is consulted before the handler runs and never
// learns that a request finished. Requests per second says nothing about how
// many run at once, so a single caller can hold every generation slot without
// ever exceeding its rate.
type ConcurrencyLimiter struct {
	limit  int
	gate   core.FeatureGate
	key    KeyExtractor
	logger *slog.Logger

	mu       sync.Mutex
	inFlight map[string]int

	rejected prometheus.Counter
	active   prometheus.Gauge
}

// NewConcurrencyLimiter builds a limiter allowing limit concurrent requests per
// key. A limit of zero disables it entirely.
func NewConcurrencyLimiter(
	limit int,
	gate core.FeatureGate,
	key KeyExtractor,
	logger *slog.Logger,
	reg *prometheus.Registry,
	namespace string,
) *ConcurrencyLimiter {
	if key == nil {
		key = PeerIPExtractor
	}

	rejected := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "concurrency_rejected_total",
		Help:      "Total requests rejected for exceeding the per-client concurrency limit.",
	})

	active := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "concurrency_active_clients",
		Help:      "Number of clients with at least one request in flight.",
	})

	if reg != nil {
		reg.MustRegister(rejected, active)
	}

	return &ConcurrencyLimiter{
		limit:    limit,
		gate:     gate,
		key:      key,
		logger:   logger,
		inFlight: make(map[string]int),
		rejected: rejected,
		active:   active,
	}
}

// UnaryServerInterceptor holds a slot for the lifetime of the handler.
func (c *ConcurrencyLimiter) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		release, ok := c.acquire(ctx)
		if !ok {
			return nil, c.reject(ctx, info.FullMethod)
		}

		defer release()

		return handler(ctx, req)
	}
}

// StreamServerInterceptor holds a slot for the lifetime of the stream.
//
// It hands the original ServerStream to the handler unchanged. The auth
// interceptor substitutes a wrapper because it derives a context carrying the
// actor; this limiter only reads the stream's own context, so there is nothing
// new to pass down. contextcheck cannot tell the two apart and is suppressed at
// the call site in cmd/easyp-svc.
func (c *ConcurrencyLimiter) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		release, ok := c.acquire(ss.Context())
		if !ok {
			return c.reject(ss.Context(), info.FullMethod)
		}

		defer release()

		return handler(srv, ss)
	}
}

// acquire reserves a slot for the calling client. The returned release must run
// when the request finishes; ok is false when the client is already at its
// limit.
func (c *ConcurrencyLimiter) acquire(
	ctx context.Context,
) (func(), bool) {
	if c.limit <= 0 {
		return func() {}, true
	}

	if c.gate != nil && !c.gate.Enabled(core.FeatureRateLimiting) {
		return func() {}, true
	}

	// An unidentifiable caller fails open, matching RateLimiter.Limit: refusing
	// everyone because the peer address is missing would be worse than not
	// limiting at all.
	key := c.key(ctx)
	if key == "" {
		return func() {}, true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inFlight[key] >= c.limit {
		return nil, false
	}

	c.inFlight[key]++
	c.active.Set(float64(len(c.inFlight)))

	return func() { c.release(key) }, true
}

// release hands the slot back and forgets the client once it goes idle, so the
// map holds only callers that are actually active and needs no background
// cleanup.
func (c *ConcurrencyLimiter) release(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inFlight[key]--
	if c.inFlight[key] <= 0 {
		delete(c.inFlight, key)
	}

	c.active.Set(float64(len(c.inFlight)))
}

// reject records the refusal and builds the error the caller sees.
//
// status.Errorf rather than a plain error for the same reason as in the auth
// interceptor: the domain-error converter only wraps the handler, so an
// unclassified error from here would surface as codes.Unknown.
func (c *ConcurrencyLimiter) reject(
	ctx context.Context,
	method string,
) error {
	c.rejected.Inc()
	c.logger.Warn("per-client concurrency limit exceeded",
		slog.String("method", method),
		slog.String("client_ip", c.key(ctx)),
		slog.Int("limit", c.limit),
	)

	return status.Errorf(
		codes.ResourceExhausted,
		"too many concurrent requests, at most %d at a time", c.limit,
	)
}
