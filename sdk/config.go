package sdk

import (
	"context"
	"crypto/tls"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// config holds the internal configuration for the client.
type config struct {
	transportCreds credentials.TransportCredentials

	// Retry
	maxRetries     int
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration

	// Timeouts
	generateCodeTimeout time.Duration
	listPluginsTimeout  time.Duration
	createPluginTimeout time.Duration

	// Interceptors
	unaryInterceptors []grpc.UnaryClientInterceptor

	// Health check
	enableHealthCheck   bool
	healthCheckInterval time.Duration

	// Keepalive
	keepaliveParams *keepalive.ClientParameters
}

func defaultConfig() *config {
	return &config{
		transportCreds:      credentials.NewTLS(&tls.Config{}),
		maxRetries:          3,
		retryBaseDelay:      100 * time.Millisecond,
		retryMaxDelay:       5 * time.Second,
		generateCodeTimeout: 30 * time.Second,
		listPluginsTimeout:  10 * time.Second,
		// CreatePlugin is slow when S3 storage is enabled: the service streams
		// the whole plugin archive from storage to compute its checksum.
		createPluginTimeout: 120 * time.Second,
		healthCheckInterval: 30 * time.Second,
	}
}

// Option configures the client.
type Option interface {
	apply(*config)
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) {
	f(c)
}

// WithInsecure disables Transport Security (TLS).
// Use this option for local development or testing.
func WithInsecure() Option {
	return optionFunc(func(c *config) {
		c.transportCreds = insecure.NewCredentials()
	})
}

// WithTransportCredentials sets custom transport credentials.
func WithTransportCredentials(creds credentials.TransportCredentials) Option {
	return optionFunc(func(c *config) {
		c.transportCreds = creds
	})
}

// WithToken authenticates the client with a write token.
//
// Reads are anonymous, so this is only needed for CreatePlugin, UpdatePlugin
// and DeletePlugin. The token travels in the authorization header, which means
// it is only as protected as the connection: pair it with TLS.
func WithToken(token string) Option {
	return optionFunc(func(c *config) {
		c.unaryInterceptors = append(c.unaryInterceptors, bearerTokenInterceptor(token))
	})
}

// bearerTokenInterceptor attaches the token to every outgoing call.
func bearerTokenInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		authedCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

		return invoker(authedCtx, method, req, reply, cc, opts...)
	}
}

// WithMaxRetries sets the maximum number of retry attempts for transient errors.
func WithMaxRetries(n int) Option {
	return optionFunc(func(c *config) {
		c.maxRetries = n
	})
}

// WithRetryBaseDelay sets the base delay between retry attempts.
func WithRetryBaseDelay(d time.Duration) Option {
	return optionFunc(func(c *config) {
		c.retryBaseDelay = d
	})
}

// WithGenerateCodeTimeout sets the default timeout for GenerateCode calls.
func WithGenerateCodeTimeout(d time.Duration) Option {
	return optionFunc(func(c *config) {
		c.generateCodeTimeout = d
	})
}

// WithListPluginsTimeout sets the default timeout for ListPlugins calls.
func WithListPluginsTimeout(d time.Duration) Option {
	return optionFunc(func(c *config) {
		c.listPluginsTimeout = d
	})
}

// WithUnaryInterceptor appends a gRPC unary client interceptor to the chain.
func WithUnaryInterceptor(i grpc.UnaryClientInterceptor) Option {
	return optionFunc(func(c *config) {
		c.unaryInterceptors = append(c.unaryInterceptors, i)
	})
}

// WithLoggingInterceptor adds a built-in logging interceptor that records
// the RPC method, call duration, and response status code.
func WithLoggingInterceptor(logger *slog.Logger) Option {
	return optionFunc(func(c *config) {
		c.unaryInterceptors = append(c.unaryInterceptors, loggingUnaryInterceptor(logger))
	})
}

// WithMetricsInterceptor adds a built-in metrics interceptor that records
// call counts, durations, and response codes via the provided MetricsCollector.
func WithMetricsInterceptor(collector MetricsCollector) Option {
	return optionFunc(func(c *config) {
		c.unaryInterceptors = append(c.unaryInterceptors, metricsUnaryInterceptor(collector))
	})
}

// WithHealthCheck enables periodic connection health monitoring with the given interval.
func WithHealthCheck(interval time.Duration) Option {
	return optionFunc(func(c *config) {
		c.enableHealthCheck = true
		c.healthCheckInterval = interval
	})
}

// WithKeepaliveParams sets gRPC keepalive parameters for the connection.
func WithKeepaliveParams(params keepalive.ClientParameters) Option {
	return optionFunc(func(c *config) {
		c.keepaliveParams = &params
	})
}
