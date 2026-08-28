package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/auth"
	"github.com/easyp-tech/service/internal/core"
)

// AuthInterceptor requires credentials on every method except an explicit
// public list.
//
// The list is an allow-list on purpose. An RPC added to the proto is protected
// until it is named here, so forgetting to update this file makes a new method
// unavailable rather than exposing it to anonymous callers.
type AuthInterceptor struct {
	authenticator auth.Authenticator
	logger        *slog.Logger
	public        map[string]struct{}
	failures      *prometheus.CounterVec
}

// Reasons recorded on the authentication failure counter.
const (
	reasonNoCredentials = "no_credentials"
	reasonUnknownToken  = "unknown_token"
)

// NewAuthInterceptor builds the interceptor and registers its failure counter.
func NewAuthInterceptor(
	authenticator auth.Authenticator,
	logger *slog.Logger,
	reg *prometheus.Registry,
	namespace string,
) *AuthInterceptor {
	failures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "auth_failures_total",
		Help:      "Total number of rejected authentication attempts.",
	}, []string{"reason"})
	reg.MustRegister(failures)

	return &AuthInterceptor{
		authenticator: authenticator,
		logger:        logger,
		failures:      failures,
		// Reads are anonymous. Health is included because grpchelper.NewServer
		// registers it on the server and probes cannot carry credentials.
		public: map[string]struct{}{
			generator.GeneratorAPI_GenerateCode_FullMethodName: {},
			generator.GeneratorAPI_Plugins_FullMethodName:      {},
			healthpb.Health_Check_FullMethodName:               {},
			healthpb.Health_Watch_FullMethodName:               {},
		},
	}
}

// UnaryServerInterceptor authenticates unary calls.
func (ai *AuthInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authedCtx, err := ai.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(authedCtx, req)
	}
}

// StreamServerInterceptor authenticates streaming calls.
func (ai *AuthInterceptor) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authedCtx, err := ai.authorize(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, &wrappedStream{ServerStream: ss, ctx: authedCtx})
	}
}

// authorize resolves the caller and returns a context carrying the actor.
//
// The error is built with status.Errorf rather than returned as a plain error.
// The domain-error converter only wraps the handler, so nothing downstream will
// classify this one, and gRPC turns an unclassified error into codes.Unknown.
func (ai *AuthInterceptor) authorize(ctx context.Context, fullMethod string) (context.Context, error) {
	if _, public := ai.public[fullMethod]; public {
		return ctx, nil
	}

	md, _ := metadata.FromIncomingContext(ctx)

	actor, err := ai.authenticator.Authenticate(ctx, md)
	if err != nil {
		ai.reject(ctx, fullMethod, err)

		return nil, status.Errorf(codes.Unauthenticated, "valid credentials are required for %s", fullMethod)
	}

	return core.WithActor(ctx, actor.Name), nil
}

// reject records a failed attempt. The reason is logged and counted, but never
// returned to the caller: telling an anonymous client whether its token was
// absent or merely wrong is free information.
func (ai *AuthInterceptor) reject(ctx context.Context, fullMethod string, err error) {
	reason := reasonUnknownToken
	if errors.Is(err, auth.ErrNoCredentials) {
		reason = reasonNoCredentials
	}

	ai.failures.WithLabelValues(reason).Inc()

	ai.logger.Warn("authentication failed",
		"method", fullMethod,
		"reason", reason,
		"caller_ip", core.CallerIPFromContext(ctx),
	)
}
