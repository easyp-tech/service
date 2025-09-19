package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/easyp-tech/easyp-plugin-server/internal/clients/docker_plugin_runner"
	"github.com/easyp-tech/easyp-plugin-server/internal/config"
	"google.golang.org/protobuf/types/pluginpb"
)

// PluginService интерфейс сервисного слоя для работы с плагинами
type PluginService interface {
	GenerateCode(ctx context.Context, codeGenRequest *pluginpb.CodeGeneratorRequest, pluginInfo string) (*pluginpb.CodeGeneratorResponse, string, error)
}

// pluginServiceImpl реализация PluginService
type pluginServiceImpl struct {
	config       *config.Config
	dockerRunner docker_plugin_runner.DockerPluginRunner
}

// NewPluginService создает новый экземпляр PluginService
func NewPluginService(cfg *config.Config, dockerRunner docker_plugin_runner.DockerPluginRunner) PluginService {
	return &pluginServiceImpl{
		config:       cfg,
		dockerRunner: dockerRunner,
	}
}

// GenerateCode генерирует код с помощью указанного плагина
func (s *pluginServiceImpl) GenerateCode(ctx context.Context, codeGenRequest *pluginpb.CodeGeneratorRequest, pluginInfo string) (*pluginpb.CodeGeneratorResponse, string, error) {
	// Валидируем входные данные
	if err := s.validateRequest(codeGenRequest, pluginInfo); err != nil {
		return nil, "validation_error", fmt.Errorf("request validation failed: %w", err)
	}

	// Вызываем плагин через Docker
	response, err := s.dockerRunner.RunPlugin(ctx, pluginInfo, codeGenRequest)
	if err != nil {
		return nil, "plugin_execution_error", fmt.Errorf("failed to run plugin %s: %w", pluginInfo, err)
	}

	// Проверяем ответ на ошибки
	if response.Error != nil {
		return response, "plugin_error", fmt.Errorf("plugin returned error: %s", *response.Error)
	}

	return response, "success", nil
}

// validateRequest проверяет корректность входных данных
func (s *pluginServiceImpl) validateRequest(request *pluginpb.CodeGeneratorRequest, pluginInfo string) error {
	if request == nil {
		return fmt.Errorf("code generator request is nil")
	}

	if pluginInfo == "" {
		return fmt.Errorf("plugin info is empty")
	}

	// Проверяем формат plugin_info (должен быть "name:version")
	parts := strings.Split(pluginInfo, ":")
	if len(parts) != 2 {
		return fmt.Errorf("plugin info must be in format 'name:version', got: %s", pluginInfo)
	}

	if parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("plugin name and version cannot be empty in: %s", pluginInfo)
	}

	// Проверяем наличие proto файлов в запросе
	if len(request.ProtoFile) == 0 {
		return fmt.Errorf("no proto files provided in request")
	}

	return nil
}
