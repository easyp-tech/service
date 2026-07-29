package grpchelper

import (
	"context"
	"log/slog"
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
	"google.golang.org/grpc/reflection"

	"github.com/easyp-tech/service/internal/core"
)

const (
	keepaliveTime    = 50 * time.Second
	keepaliveTimeout = 10 * time.Second
	keepaliveMinTime = 30 * time.Second
)

// Metrics provides panic counter for recovery interceptor.
type Metrics interface {
	PanicsTotal() prometheus.Counter
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
) (*grpc.Server, *health.Server) {
	unaryInterceptor := buildUnaryInterceptors(metr, log, serverMetrics, converter, extraUnary)
	streamInterceptor := buildStreamInterceptors(metr, log, serverMetrics, converter, extraStream)

	server := grpc.NewServer(
		grpc.Creds(creds),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
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

	reflection.Register(server)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)

	return server, healthServer
}

func buildUnaryInterceptors(
	metr Metrics,
	log *slog.Logger,
	serverMetrics *grpc_prometheus.ServerMetrics,
	converter GRPCCodesConverterHandler,
	extraUnary []grpc.UnaryServerInterceptor,
) []grpc.UnaryServerInterceptor {
	loggingOpts := []logging.Option{
		logging.WithLogOnEvents(
			logging.StartCall,
			logging.FinishCall,
			logging.PayloadReceived,
			logging.PayloadSent,
		),
	}

	chain := []grpc.UnaryServerInterceptor{
		TraceLoggingUnaryServerInterceptor(log),
		realip.UnaryServerInterceptor(nil, nil),
		callerIPUnaryInterceptor(),
		serverMetrics.UnaryServerInterceptor(),
		logging.UnaryServerInterceptor(interceptorLogger(log), loggingOpts...),
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
) []grpc.StreamServerInterceptor {
	loggingOpts := []logging.Option{
		logging.WithLogOnEvents(
			logging.StartCall,
			logging.FinishCall,
			logging.PayloadReceived,
			logging.PayloadSent,
		),
	}

	chain := []grpc.StreamServerInterceptor{
		TraceLoggingStreamServerInterceptor(log),
		realip.StreamServerInterceptor(nil, nil),
		callerIPStreamInterceptor(),
		serverMetrics.StreamServerInterceptor(),
		logging.StreamServerInterceptor(interceptorLogger(log), loggingOpts...),
		grpc_recovery.StreamServerInterceptor(grpc_recovery.WithRecoveryHandlerContext(recoveryFunc(metr, errInternal))),
		grpc_validator.StreamServerInterceptor(),
	}
	chain = append(chain, extraStream...)

	// Innermost, for the same reason as the unary chain above.
	return append(chain, StreamConvertCodesServerInterceptor(converter))
}

func extractCallerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "unknown"
	}

	return p.Addr.String()
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
