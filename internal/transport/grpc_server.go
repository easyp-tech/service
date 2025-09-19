package transport

import (
	"context"
	"log/slog"
	"net"

	plugingeneratorv1 "github.com/easyp-tech/easyp-plugin-server/api/plugin-generator/v1"
	"github.com/easyp-tech/easyp-plugin-server/internal/config"
	"github.com/easyp-tech/easyp-plugin-server/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GRPCServer представляет gRPC сервер
type GRPCServer struct {
	plugingeneratorv1.UnimplementedPluginGeneratorServiceServer
	config        *config.Config
	pluginService service.PluginService
}

// NewGRPCServer создает новый gRPC сервер
func NewGRPCServer(cfg *config.Config, pluginService service.PluginService) *GRPCServer {
	return &GRPCServer{
		config:        cfg,
		pluginService: pluginService,
	}
}

// GenerateCode реализует метод PluginGeneratorService
func (s *GRPCServer) GenerateCode(ctx context.Context, req *plugingeneratorv1.GenerateCodeRequest) (*plugingeneratorv1.GenerateCodeResponse, error) {
	slog.Info("Received GenerateCode request",
		"plugin_info", req.GetPluginInfo(),
		"proto_files_count", len(req.GetCodeGeneratorRequest().GetProtoFile()),
	)

	// Вызываем сервисный слой
	codeGenResponse, statusMsg, err := s.pluginService.GenerateCode(
		ctx,
		req.GetCodeGeneratorRequest(),
		req.GetPluginInfo(),
	)

	if err != nil {
		slog.Error("Failed to generate code",
			"plugin_info", req.GetPluginInfo(),
			"error", err,
			"status", statusMsg,
		)

		// Определяем gRPC код ошибки на основе статуса
		grpcCode := s.mapStatusToGRPCCode(statusMsg)
		return nil, status.Errorf(grpcCode, "code generation failed: %v", err)
	}

	slog.Info("Code generation completed successfully",
		"plugin_info", req.GetPluginInfo(),
		"generated_files_count", len(codeGenResponse.GetFile()),
	)

	// Формируем успешный ответ
	response := &plugingeneratorv1.GenerateCodeResponse{
		CodeGeneratorResponse: codeGenResponse,
		Status:                statusMsg,
		Message:               "Code generation completed successfully",
	}

	return response, nil
}

// mapStatusToGRPCCode мапит внутренние статусы на gRPC коды
func (s *GRPCServer) mapStatusToGRPCCode(status string) codes.Code {
	switch status {
	case "validation_error":
		return codes.InvalidArgument
	case "plugin_execution_error":
		return codes.Internal
	case "plugin_error":
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}

// Start запускает gRPC сервер
func (s *GRPCServer) Start() error {
	// Создаем listener
	listener, err := net.Listen("tcp", s.config.Server.Address())
	if err != nil {
		return err
	}

	// Создаем gRPC сервер
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(s.loggingInterceptor),
	)

	// Регистрируем сервис
	plugingeneratorv1.RegisterPluginGeneratorServiceServer(grpcServer, s)

	slog.Info("Starting gRPC server", "address", s.config.Server.Address())

	// Запускаем сервер
	return grpcServer.Serve(listener)
}

// loggingInterceptor добавляет логирование для всех запросов
func (s *GRPCServer) loggingInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	slog.Info("gRPC request started", "method", info.FullMethod)

	resp, err := handler(ctx, req)

	if err != nil {
		slog.Error("gRPC request failed",
			"method", info.FullMethod,
			"error", err,
		)
	} else {
		slog.Info("gRPC request completed", "method", info.FullMethod)
	}

	return resp, err
}
