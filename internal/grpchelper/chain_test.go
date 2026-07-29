package grpchelper_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/easyp-tech/service/internal/grpchelper"
)

// errDomain stands in for an error a handler would return.
var errDomain = errors.New("something went wrong in the domain")

// panicMetrics satisfies grpchelper.Metrics without touching a registry.
type panicMetrics struct{ counter prometheus.Counter }

func (m panicMetrics) PanicsTotal() prometheus.Counter { return m.counter }

// convertDomain stands in for api.ErrorToStatus: it classifies domain errors and
// calls everything else Internal, which is what the real one does.
func convertDomain(err error) *status.Status {
	if errors.Is(err, errDomain) {
		return status.New(codes.FailedPrecondition, err.Error())
	}

	return status.New(codes.Internal, err.Error())
}

// TestExtraInterceptorStatusSurvivesConversion pins the interceptor order.
//
// The converter turns unrecognised errors into Internal. Placed outside the
// extra interceptors it would relabel their statuses, so an authentication
// failure reached clients as Internal instead of Unauthenticated — which is
// what happened before the order was fixed, and it degraded rate limiting the
// same way.
func TestExtraInterceptorStatusSurvivesConversion(t *testing.T) {
	t.Parallel()

	rejecting := func(
		_ context.Context,
		_ any,
		_ *grpc.UnaryServerInfo,
		_ grpc.UnaryHandler,
	) (any, error) {
		return nil, status.Error(codes.Unauthenticated, "no credentials")
	}

	require.Equal(t, codes.Unauthenticated, callHealth(t, []grpc.UnaryServerInterceptor{rejecting}))
}

// TestExtraInterceptorPassesThrough is the control: with an interceptor that
// does nothing, the same call succeeds.
func TestExtraInterceptorPassesThrough(t *testing.T) {
	t.Parallel()

	passing := func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		return handler(ctx, req)
	}

	require.Equal(t, codes.OK, callHealth(t, []grpc.UnaryServerInterceptor{passing}))
}

// callHealth builds a real server with the given extra interceptors and calls
// its built-in health service over an in-memory connection, so the assertions
// cover the chain grpc actually assembles rather than a reimplementation of it.
// It returns the status code, which is the only thing under test.
func callHealth(t *testing.T, extra []grpc.UnaryServerInterceptor) codes.Code {
	t.Helper()

	srv, healthSrv := grpchelper.NewServer(
		panicMetrics{counter: prometheus.NewCounter(prometheus.CounterOpts{ //nolint:exhaustruct
			Name: "panics_total",
			Help: "Panics recovered during the test.",
		})},
		slog.New(slog.DiscardHandler),
		grpc_prometheus.NewServerMetrics(),
		convertDomain,
		insecure.NewCredentials(),
		extra,
		nil,
	)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	listener := bufconn.Listen(1024 * 1024)

	go func() { _ = srv.Serve(listener) }()

	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	_, err = healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{}) //nolint:exhaustruct

	return status.Code(err)
}
