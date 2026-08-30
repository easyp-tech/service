// Package api implements the API server for the application.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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

// errorDomain identifies whose reason vocabulary the ErrorInfo below uses.
const errorDomain = "easyp.tech"

// Reasons attached to every non-OK status as google.rpc.ErrorInfo.
//
// A gRPC code is a category — NotFound covers a missing plugin and a missing
// archive alike — and the message is prose that changes when someone rewords
// it. Neither is something a client can branch on. These strings are the part
// of the error contract that is promised to stay: they are documented in
// generator.proto and adding one is a compatible change, while changing what an
// existing one means is not.
const (
	ReasonNotFound           = "NOT_FOUND"
	ReasonInvalidPluginName  = "INVALID_PLUGIN_NAME"
	ReasonInvalidConfig      = "INVALID_CONFIG"
	ReasonGenerationFailed   = "GENERATION_FAILED"
	ReasonServerOverloaded   = "SERVER_OVERLOADED"
	ReasonAlreadyExists      = "ALREADY_EXISTS"
	ReasonMaxPluginsExceeded = "MAX_PLUGINS_EXCEEDED"
	ReasonShuttingDown       = "SHUTTING_DOWN"
	ReasonStorageUnavailable = "STORAGE_UNAVAILABLE"
	ReasonBinaryNotUploaded  = "BINARY_NOT_UPLOADED"
	ReasonFeatureDenied      = "FEATURE_DENIED"
	ReasonDeadlineExceeded   = "DEADLINE_EXCEEDED"
	ReasonCanceled           = "CANCELED"
	ReasonInternal           = "INTERNAL"
)

// callSitePrefix matches one leading "identifier: " segment of a wrapped Go
// error — "api.app.Generate: ", "c.registry.Create: ", "ValidateConfig: ".
//
// Every layer here wraps with fmt.Errorf("<call>: %w", err), which is right for
// a log and wrong for a client: the message that reached the wire spelled out
// the service's internal call graph, and once clients started reading it that
// spelling was a public interface. A real message carries spaces, so requiring
// the segment to have none is enough to tell a call site from prose.
var callSitePrefix = regexp.MustCompile(`^[A-Za-z_(][A-Za-z0-9_.*()\[\]]*: `)

// clientMessage strips the internal call chain from an error, leaving the part
// that describes what actually went wrong.
func clientMessage(err error) string {
	msg := err.Error()

	for {
		trimmed := callSitePrefix.ReplaceAllString(msg, "")
		if trimmed == msg {
			return msg
		}

		msg = trimmed
	}
}

// ErrorToStatus converts an application error to a gRPC status.
// Compatible with grpchelper.GRPCCodesConverterHandler.
func ErrorToStatus(err error) *status.Status {
	if err == nil {
		return status.New(codes.OK, "")
	}

	code, reason := classify(err)

	st := status.New(code, clientMessage(err))

	// A status that cannot carry its details is still a usable status, so a
	// failure here is dropped rather than replacing a specific error with a
	// vaguer one about attaching details to it.
	withDetails, detailErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: errorDomain,
	})
	if detailErr != nil {
		return st
	}

	return withDetails
}

// classify maps an application error to its gRPC code and stable reason.
func classify(err error) (codes.Code, string) {
	code, reason := codes.Internal, ReasonInternal

	switch {
	case errors.Is(err, core.ErrNotFound):
		code, reason = codes.NotFound, ReasonNotFound
	case errors.Is(err, core.ErrInvalidPluginName):
		code, reason = codes.InvalidArgument, ReasonInvalidPluginName
	case errors.Is(err, core.ErrInvalidConfig):
		code, reason = codes.InvalidArgument, ReasonInvalidConfig
	case errors.Is(err, core.ErrGenerationFailed):
		code, reason = codes.Internal, ReasonGenerationFailed
	case errors.Is(err, core.ErrServerOverloaded):
		code, reason = codes.ResourceExhausted, ReasonServerOverloaded
	case errors.Is(err, core.ErrAlreadyExists):
		code, reason = codes.AlreadyExists, ReasonAlreadyExists
	case errors.Is(err, core.ErrMaxPluginsExceeded):
		code, reason = codes.ResourceExhausted, ReasonMaxPluginsExceeded
	case errors.Is(err, core.ErrShuttingDown):
		code, reason = codes.Unavailable, ReasonShuttingDown
	case errors.Is(err, core.ErrStorageUnavailable):
		code, reason = codes.Unavailable, ReasonStorageUnavailable
	case errors.Is(err, core.ErrBinaryNotUploaded):
		code, reason = codes.FailedPrecondition, ReasonBinaryNotUploaded
	case errors.Is(err, core.ErrFeatureDenied):
		code, reason = codes.PermissionDenied, ReasonFeatureDenied
	case errors.Is(err, context.DeadlineExceeded):
		code, reason = codes.DeadlineExceeded, ReasonDeadlineExceeded
	case errors.Is(err, context.Canceled):
		code, reason = codes.Canceled, ReasonCanceled
	}

	return code, reason
}
