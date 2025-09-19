package docker_plugin_runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/easyp-tech/easyp-plugin-server/internal/config"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// runnerImpl реализация DockerPluginRunner
type runnerImpl struct {
	config  *config.Config
	timeout time.Duration
}

// New создает новый экземпляр DockerPluginRunner
func New(cfg *config.Config, timeout time.Duration) DockerPluginRunner {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &runnerImpl{
		config:  cfg,
		timeout: timeout,
	}
}

// RunPlugin запускает плагин в Docker контейнере
func (r *runnerImpl) RunPlugin(ctx context.Context, pluginInfo string, request *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	// Формируем имя образа с помощью конфига
	imageName := r.config.Registry.GetImageName(pluginInfo)

	// Создаем контекст с таймаутом
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Конвертируем запрос в бинарный protobuf формат
	requestData, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request to protobuf: %w", err)
	}

	// Подготавливаем команду Docker
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm", "-i",
		"--network=none", // Изолируем сеть для безопасности
		"--memory=512m",  // Ограничиваем память
		"--cpus=1.0",     // Ограничиваем CPU
		imageName,
	)

	// Передаем бинарные данные protobuf через stdin
	cmd.Stdin = bytes.NewReader(requestData)

	// Выполняем команду и получаем результат
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("plugin execution failed: %s, stderr: %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("failed to execute plugin: %w", err)
	}

	// Парсим ответ из бинарного protobuf формата
	var response pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response from protobuf: %w", err)
	}

	return &response, nil
}
