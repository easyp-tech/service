package grpchelper

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"time"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/realip"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	grpc_validator "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/validator"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"

	"github.com/easyp-tech/service/internal/core"
)

const (
	keepaliveTime    = 50 * time.Second
	keepaliveTimeout = 10 * time.Second
	keepaliveMinTime = 30 * time.Second
)

// Transport limits applied when ServerOptions leaves them unset. They are
// defaults here rather than only in the config so that forgetting to pass them
// yields a bounded server instead of gRPC's own, which caps received messages
// at 4 MiB and leaves concurrent streams and sent messages effectively
// unlimited.
const (
	// DefaultMaxMsgSize matches registry.max_output_size: a plugin allowed to
	// produce 64 MiB must be able to have it delivered, and a request built
	// from a large proto tree passes the same way in.
	DefaultMaxMsgSize = 64 << 20
	// DefaultMaxConcurrentStreams bounds what a single connection can occupy.
	// gRPC's default is math.MaxUint32, so one client could open streams until
	// the process ran out of memory, each allocating buffers and goroutines
	// before any limiter in the chain got to see it.
	DefaultMaxConcurrentStreams = 256
)

// Metrics provides panic counter for recovery interceptor.
type Metrics interface {
	PanicsTotal() prometheus.Counter
}

// ServerOptions carries the settings NewServer cannot pick on its own. The zero
// value is usable: every field falls back to the defaults above.
type ServerOptions struct {
	// TrustedProxies are the CIDRs whose forwarding headers may be believed.
	// Empty means the connecting peer is the caller.
	TrustedProxies []netip.Prefix
	// MaxRecvMsgSize and MaxSendMsgSize bound a single message in each
	// direction. Zero means DefaultMaxMsgSize.
	MaxRecvMsgSize int
	MaxSendMsgSize int
	// MaxConcurrentStreams bounds concurrent streams per connection.
	// Zero means DefaultMaxConcurrentStreams.
	MaxConcurrentStreams uint32
}

func (o ServerOptions) withDefaults() ServerOptions {
	if o.MaxRecvMsgSize <= 0 {
		o.MaxRecvMsgSize = DefaultMaxMsgSize
	}

	if o.MaxSendMsgSize <= 0 {
		o.MaxSendMsgSize = DefaultMaxMsgSize
	}

	if o.MaxConcurrentStreams == 0 {
		o.MaxConcurrentStreams = DefaultMaxConcurrentStreams
	}

	return o
}

// NewServer creates and returns a gRPC server.
// Transport security comes from creds; build it with BuildServerCreds.
func NewServer(
	metr Metrics,
	log *slog.Logger,
	serverMetrics *grpc_prometheus.ServerMetrics,
	converter GRPCCodesConverterHandler,
	creds credentials.TransportCredentials,
	extraUnary []grpc.UnaryServerInterceptor,
	extraStream []grpc.StreamServerInterceptor,
	opts ServerOptions,
) (*grpc.Server, *health.Server) {
	opts = opts.withDefaults()

	unaryInterceptor := buildUnaryInterceptors(metr, log, serverMetrics, converter, extraUnary, opts.TrustedProxies)
	streamInterceptor := buildStreamInterceptors(metr, log, serverMetrics, converter, extraStream, opts.TrustedProxies)

	server := grpc.NewServer(
		grpc.Creds(creds),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(opts.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(opts.MaxSendMsgSize),
		grpc.MaxConcurrentStreams(opts.MaxConcurrentStreams),
		grpc.KeepaliveParams(
			keepalive.ServerParameters{ //nolint:exhaustruct
				Time:    keepaliveTime,
				Timeout: keepaliveTimeout,
			},
		),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(
			unaryInterceptor...,
		),
		grpc.ChainStreamInterceptor(
			streamInterceptor...,
		),
	)

	// Server reflection is deliberately not registered: it enumerates every
	// method and message type to anyone who asks, and this listener faces the
	// internet. Clients use the generated stubs; ad-hoc tooling passes --proto.

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)

	return server, healthServer
}

// realIPOptions configures how the caller's address is decided.
//
// Headers are consulted only for connections originating inside trustedProxies;
// for anyone else the connecting address stands, so a caller cannot promote
// itself by sending X-Forwarded-For. With no trusted proxies configured the
// library short-circuits to the peer address and never reads a header at all,
// which is the right answer for a listener clients reach directly.
func realIPOptions(trustedProxies []netip.Prefix) []realip.Option {
	return []realip.Option{
		realip.WithTrustedPeers(trustedProxies),
		realip.WithTrustedProxies(trustedProxies),
		// X-Real-IP is listed first deliberately. The library walks the headers
		// in order, but its X-Forwarded-For branch returns whatever it found —
		// including nothing — instead of falling through, so any header after
		// X-Forwarded-For is unreachable. With this order both are honoured:
		// an absent X-Real-IP fails to parse and the loop moves on. Both come
		// from the same trusted proxy, so preferring one is a matter of which
		// is readable, not which is safer.
		realip.WithHeaders([]string{realip.XRealIp, realip.XForwardedFor}),
	}
}

// loggingOptions returns the settings both interceptor chains log under. It is
// one function rather than two identical literals so that unary and streaming
// cannot drift apart: what follows is a decision about what leaves the process,
// and it should hold for every RPC, not most of them.
//
// PayloadReceived and PayloadSent are deliberately absent. The library logs
// them at the level the response code maps to, which for a successful call is
// Info — so with the chart's default log level every request wrote the caller's
// entire CodeGeneratorRequest, and every response the source it generated, into
// stdout. That is customers' proto definitions and generated code in whatever
// aggregator collects logs, on top of a volume no retention policy survives.
// Method, code and duration are what an operator actually reads; the contents
// belong in a trace on a machine that is meant to have them.
func loggingOptions() []logging.Option {
	return []logging.Option{
		logging.WithLogOnEvents(
			logging.StartCall,
			logging.FinishCall,
		),
	}
}

func buildUnaryInterceptors(
	metr Metrics,
	log *slog.Logger,
	serverMetrics *grpc_prometheus.ServerMetrics,
	converter GRPCCodesConverterHandler,
	extraUnary []grpc.UnaryServerInterceptor,
	trustedProxies []netip.Prefix,
) []grpc.UnaryServerInterceptor {
	chain := []grpc.UnaryServerInterceptor{
		TraceLoggingUnaryServerInterceptor(log),
		realip.UnaryServerInterceptorOpts(realIPOptions(trustedProxies)...),
		callerIPUnaryInterceptor(),
		serverMetrics.UnaryServerInterceptor(),
		logging.UnaryServerInterceptor(interceptorLogger(log), loggingOptions()...),
		grpc_recovery.UnaryServerInterceptor(grpc_recovery.WithRecoveryHandlerContext(recoveryFunc(metr, errInternal))),
		grpc_validator.UnaryServerInterceptor(),
	}
	chain = append(chain, extraUnary...)

	// The code converter goes last, which makes it the innermost interceptor and
	// therefore the only one wrapping the handler alone. It translates domain
	// errors into gRPC codes; interceptors already speak gRPC, so running it
	// outside them would relabel their statuses as Internal.
	return append(chain, UnaryConvertCodesServerInterceptor(converter))
}

func buildStreamInterceptors(
	metr Metrics,
	log *slog.Logger,
	serverMetrics *grpc_prometheus.ServerMetrics,
	converter GRPCCodesConverterHandler,
	extraStream []grpc.StreamServerInterceptor,
	trustedProxies []netip.Prefix,
) []grpc.StreamServerInterceptor {
	chain := []grpc.StreamServerInterceptor{
		TraceLoggingStreamServerInterceptor(log),
		realip.StreamServerInterceptorOpts(realIPOptions(trustedProxies)...),
		callerIPStreamInterceptor(),
		serverMetrics.StreamServerInterceptor(),
		logging.StreamServerInterceptor(interceptorLogger(log), loggingOptions()...),
		grpc_recovery.StreamServerInterceptor(grpc_recovery.WithRecoveryHandlerContext(recoveryFunc(metr, errInternal))),
		grpc_validator.StreamServerInterceptor(),
	}
	chain = append(chain, extraStream...)

	// Innermost, for the same reason as the unary chain above.
	return append(chain, StreamConvertCodesServerInterceptor(converter))
}

// CallerIPUnknown is what extractCallerIP reports when no address could be
// determined at all. It is exported because consumers must be able to tell it
// apart from a real address: treating it as one would file every anonymous
// caller under a single identity, which for a rate limiter means one shared
// bucket and for an audit log means a fabricated actor.
const CallerIPUnknown = "unknown"

// extractCallerIP resolves who is calling, preferring the address realip
// established from a trusted proxy's headers over the connecting peer.
//
// The port is stripped. It used to be included, which made every audit row
// carry an ephemeral port number, and would make any per-caller limit keyed on
// this value unique per connection — that is, no limit at all.
func extractCallerIP(ctx context.Context) string {
	if ip, ok := realip.FromContext(ctx); ok && ip.IsValid() {
		return ip.String()
	}

	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return CallerIPUnknown
	}

	addr := p.Addr.String()

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port — a unix socket or a bufconn in tests. Whatever it is,
		// it is already the whole identity.
		return addr
	}

	return host
}

func callerIPUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		return handler(core.WithCallerIP(ctx, extractCallerIP(ctx)), req)
	}
}

func callerIPStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := core.WithCallerIP(ss.Context(), extractCallerIP(ss.Context()))
		wrapped := &wrappedServerStream{ServerStream: ss, ctx: ctx}

		return handler(srv, wrapped)
	}
}
