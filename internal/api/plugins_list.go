package api

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

// listPlugins is the one implementation behind both transports of the plugin
// listing — the gRPC handler and the MCP tool. They used to duplicate the
// filter mapping and response building, which is two places for the pagination
// contract to drift apart.
func listPlugins(ctx context.Context, svc core.Service, req *generator.PluginsRequest) (*generator.PluginsResponse, error) {
	if req == nil {
		req = &generator.PluginsRequest{}
	}

	after, err := decodePageToken(strings.TrimSpace(req.GetPageToken()))
	if err != nil {
		return nil, err
	}

	filter := core.PluginFilter{
		Group:   strings.TrimSpace(req.GetGroup()),
		Name:    strings.TrimSpace(req.GetName()),
		Version: strings.TrimSpace(req.GetVersion()),
		Tags:    compactStrings(req.GetTags()),
	}

	page := core.PluginPage{
		Size:  int(req.GetPageSize()),
		After: after,
	}

	list, err := svc.ListPlugins(ctx, filter, page)
	if err != nil {
		return nil, fmt.Errorf("ListPlugins: %w", err)
	}

	// No count field: it used to carry len(plugins) under the name `total`,
	// which reads as the size of the collection and is not. A client that wants
	// the page size has it in the slice.
	response := &generator.PluginsResponse{
		Plugins:       make([]*generator.PluginInfo, 0, len(list.Plugins)),
		NextPageToken: encodePageToken(list.Next),
	}

	for _, plugInfo := range list.Plugins {
		response.Plugins = append(response.Plugins, &generator.PluginInfo{
			Id:        plugInfo.ID.String(),
			Group:     plugInfo.Group,
			Name:      plugInfo.Name,
			Version:   plugInfo.Version,
			Tags:      append([]string(nil), plugInfo.Tags...),
			CreatedAt: timestamppb.New(plugInfo.CreatedAt),
		})
	}

	return response, nil
}
