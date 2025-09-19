package docker_plugin_runner

import (
	"context"

	"google.golang.org/protobuf/types/pluginpb"
)

// DockerPluginRunner интерфейс для запуска плагинов в Docker
type DockerPluginRunner interface {
	RunPlugin(ctx context.Context, pluginInfo string, request *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error)
}
