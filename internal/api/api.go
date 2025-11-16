// Package server implements the API server for the application.
package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sipki-tech/dev-platform/grpc_helper"
	"github.com/sipki-tech/dev-platform/logger"
	"github.com/sipki-tech/dev-platform/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/api/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

var _ generator.ServiceAPIServer = (*API)(nil)

// API provides the API server implementation.
type API struct {
	app *core.Core
}

// New creates and returns gRPC server.
func New(ctx context.Context, m metrics.Metrics, applications *core.Core, reg *prometheus.Registry, namespace string) *grpc.Server {
	log := logger.FromContext(ctx)
	subsystem := "api"

	grpcMetrics := grpc_helper.NewServerMetrics(reg, namespace, subsystem)

	srv, health := grpc_helper.NewServer(m, log, grpcMetrics, apiError,
		[]grpc.UnaryServerInterceptor{},
		[]grpc.StreamServerInterceptor{},
	)
	health.SetServingStatus(generator.ServiceAPI_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)

	api := &API{
		app: applications,
	}
	generator.RegisterServiceAPIServer(srv, api)

	return srv
}

// GenerateCode implements generator.PluginGeneratorServiceServer.
func (api *API) GenerateCode(ctx context.Context, request *generator.GenerateCodeRequest) (*generator.GenerateCodeResponse, error) {
	resp, err := api.app.Generate(ctx, core.GenerateCodeRequest{
		PluginName: request.PluginName,
		Payload:    request.CodeGeneratorRequest,
	})
	if err != nil {
		return nil, fmt.Errorf("api.app.Generate: %w", err)
	}

	return &generator.GenerateCodeResponse{
		CodeGeneratorResponse: resp.Payload,
	}, nil
}

func apiError(err error) *status.Status {
	if err == nil {
		return nil
	}

	code := codes.Internal
	switch {
	case errors.Is(err, core.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, core.ErrInvalidPluginName):
		code = codes.InvalidArgument
	case errors.Is(err, core.ErrGenerationFailed):
		code = codes.Internal
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	}

	return status.New(code, err.Error())
}
