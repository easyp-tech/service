package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
)

// newMCPHandler builds the read-only MCP surface: plugin discovery, and nothing
// else.
//
// The easyp.yaml schema tool that used to sit beside it came from
// easyp/mcp/easypconfig, and taking that package meant taking the easyp module
// — which imports this service's own generated API to call it for remote plugin
// execution. That import resolved to the main module while the contract lived
// in it; once api/ became its own module under a renamed package, easyp asked
// for a path nothing provides and the service stopped building. A service that
// cannot compile without its own client is the wrong shape regardless, so the
// dependency is gone rather than worked around.
//
// The tool is worth having back. It belongs in a library both sides can depend
// on, below them in the graph — its only tie to easyp internals is one call for
// the list of lint rule names.
func newMCPHandler(ps core.Service, logger *slog.Logger) http.Handler {
	opts := &mcp.ServerOptions{
		Instructions: "EasyP MCP server with plugin discovery.",
	}
	if logger != nil {
		opts.Logger = logger
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "easyp-service-mcp",
		Version: "v1.0.0",
	}, opts)

	err := generator.RegisterGeneratorAPITools(srv, newPluginToolHandler(ps))
	if err != nil {
		panic(fmt.Errorf("register generator MCP tools: %w", err))
	}

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		Logger: logger,
	})
}

// MCPHandler returns the MCP streamable HTTP handler.
func (api *API) MCPHandler() http.Handler {
	return api.mcpHandler
}
