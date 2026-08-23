package api

import (
	"context"

	"github.com/easyp-tech/service/api/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

type pluginToolHandler struct {
	ps core.Service
}

func newPluginToolHandler(ps core.Service) *pluginToolHandler {
	return &pluginToolHandler{ps: ps}
}

func (h *pluginToolHandler) Plugins(ctx context.Context, req *generator.PluginsRequest) (*generator.PluginsResponse, error) {
	if h == nil || h.ps == nil {
		return &generator.PluginsResponse{
			Plugins: make([]*generator.PluginInfo, 0),
		}, nil
	}

	return listPlugins(ctx, h.ps, req)
}
