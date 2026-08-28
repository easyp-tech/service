// Package api implements the API server for the application.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

var _ generator.GeneratorAPIServer = (*API)(nil)

// API provides the API server implementation.
type API struct {
	app        core.Service
	mcpHandler http.Handler
}

// New registers the API handler on the given gRPC server, sets the health
// serving status, and wires up the MCP HTTP handler.
// The gRPC server and health server are expected to be created
// externally (e.g. via grpchelper.NewServer).
func New(grpcSrv *grpc.Server, healthSrv *health.Server, applications core.Service, logger *slog.Logger) *API {
	healthSrv.SetServingStatus(generator.GeneratorAPI_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)

	api := &API{
		app:        applications,
		mcpHandler: newMCPHandler(applications, logger),
	}
	generator.RegisterGeneratorAPIServer(grpcSrv, api)

	return api
}

// GenerateCode implements generator.PluginGeneratorServiceServer.
func (api *API) GenerateCode(ctx context.Context, request *generator.GenerateCodeRequest) (*generator.GenerateCodeResponse, error) {
	resp, err := api.app.Generate(ctx, core.GenerateCodeRequest{
		PluginName: request.GetPluginName(),
		Payload:    request.GetCodeGeneratorRequest(),
	})
	if err != nil {
		return nil, fmt.Errorf("api.app.Generate: %w", err)
	}

	return &generator.GenerateCodeResponse{
		CodeGeneratorResponse: resp.Payload,
	}, nil
}

func (api *API) Plugins(ctx context.Context, request *generator.PluginsRequest) (*generator.PluginsResponse, error) {
	response, err := listPlugins(ctx, api.app, request)
	if errors.Is(err, errBadPageToken) {
		return nil, status.Error(codes.InvalidArgument, err.Error()) //nolint:wrapcheck // the status must reach the client as built
	}

	if err != nil {
		return nil, fmt.Errorf("api.app.ListPlugins: %w", err)
	}

	return response, nil
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}

	return out
}

func (api *API) CreatePlugin(ctx context.Context, request *generator.CreatePluginRequest) (*generator.CreatePluginResponse, error) {
	var configJSON json.RawMessage
	if request.GetConfig() != nil {
		b, err := protojson.Marshal(request.GetConfig())
		if err != nil {
			return nil, fmt.Errorf("protojson.Marshal config: %w", err)
		}
		configJSON = b
	}

	info, err := api.app.CreatePlugin(ctx, core.CreatePluginRequest{
		Group:   strings.TrimSpace(request.GetGroup()),
		Name:    strings.TrimSpace(request.GetName()),
		Version: strings.TrimSpace(request.GetVersion()),
		Config:  configJSON,
		Tags:    compactStrings(request.GetTags()),
	})
	if err != nil {
		return nil, fmt.Errorf("api.app.CreatePlugin: %w", err)
	}

	return &generator.CreatePluginResponse{
		Plugin: pluginInfoToProto(info),
	}, nil
}

func (api *API) UpdatePlugin(ctx context.Context, request *generator.UpdatePluginRequest) (*generator.UpdatePluginResponse, error) {
	var configJSON json.RawMessage
	if request.GetConfig() != nil {
		b, err := protojson.Marshal(request.GetConfig())
		if err != nil {
			return nil, fmt.Errorf("protojson.Marshal config: %w", err)
		}
		configJSON = b
	}

	updateConfig, updateTags, maskErr := updateMaskFields(request.GetUpdateMask())
	if maskErr != nil {
		return nil, maskErr
	}

	info, err := api.app.UpdatePlugin(ctx, core.UpdatePluginRequest{
		Group:        strings.TrimSpace(request.GetGroup()),
		Name:         strings.TrimSpace(request.GetName()),
		Version:      strings.TrimSpace(request.GetVersion()),
		Config:       configJSON,
		Tags:         compactStrings(request.GetTags()),
		UpdateConfig: updateConfig,
		UpdateTags:   updateTags,
	})
	if err != nil {
		return nil, fmt.Errorf("api.app.UpdatePlugin: %w", err)
	}

	return &generator.UpdatePluginResponse{
		Plugin: pluginInfoToProto(info),
	}, nil
}

// updateMaskFields resolves an UpdatePlugin field mask to the two fields this
// service can replace.
//
// An absent or empty mask means both, which is what UpdatePlugin did before the
// mask existed — so a client written against the older contract keeps working
// unchanged.
//
// An unknown path is refused rather than ignored. A mask is the caller stating
// what it intends to change; silently dropping a path it named would apply some
// other update than the one asked for, and "tag" instead of "tags" is exactly
// the kind of thing that gets typed.
func updateMaskFields(mask *fieldmaskpb.FieldMask) (updateConfig, updateTags bool, err error) {
	paths := mask.GetPaths()
	if len(paths) == 0 {
		return true, true, nil
	}

	for _, path := range paths {
		switch strings.TrimSpace(path) {
		case "config":
			updateConfig = true
		case "tags":
			updateTags = true
		default:
			return false, false, status.Errorf(
				codes.InvalidArgument,
				"update_mask: unknown path %q; the paths are \"config\" and \"tags\"",
				path,
			)
		}
	}

	return updateConfig, updateTags, nil
}

func (api *API) DeletePlugin(ctx context.Context, request *generator.DeletePluginRequest) (*generator.DeletePluginResponse, error) {
	err := api.app.DeletePlugin(ctx,
		strings.TrimSpace(request.GetGroup()),
		strings.TrimSpace(request.GetName()),
		strings.TrimSpace(request.GetVersion()),
	)
	if err != nil {
		return nil, fmt.Errorf("api.app.DeletePlugin: %w", err)
	}

	return &generator.DeletePluginResponse{}, nil
}

func pluginInfoToProto(info *core.PluginInfo) *generator.PluginInfo {
	return &generator.PluginInfo{
		Id:        info.ID.String(),
		Group:     info.Group,
		Name:      info.Name,
		Version:   info.Version,
		Tags:      info.Tags,
		CreatedAt: timestamppb.New(info.CreatedAt),
	}
}

// ErrorToStatus converts an application error to a gRPC status.
// Compatible with grpchelper.GRPCCodesConverterHandler.
func ErrorToStatus(err error) *status.Status {
	if err == nil {
		return status.New(codes.OK, "")
	}

	code := codes.Internal
	switch {
	case errors.Is(err, core.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, core.ErrInvalidPluginName):
		code = codes.InvalidArgument
	case errors.Is(err, core.ErrInvalidConfig):
		code = codes.InvalidArgument
	case errors.Is(err, core.ErrGenerationFailed):
		code = codes.Internal
	case errors.Is(err, core.ErrServerOverloaded):
		code = codes.ResourceExhausted
	case errors.Is(err, core.ErrAlreadyExists):
		code = codes.AlreadyExists
	case errors.Is(err, core.ErrMaxPluginsExceeded):
		code = codes.ResourceExhausted
	case errors.Is(err, core.ErrShuttingDown):
		code = codes.Unavailable
	case errors.Is(err, core.ErrStorageUnavailable):
		code = codes.Unavailable
	case errors.Is(err, core.ErrBinaryNotUploaded):
		code = codes.FailedPrecondition
	case errors.Is(err, core.ErrFeatureDenied):
		code = codes.PermissionDenied
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	}

	return status.New(code, err.Error())
}
