package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	var endpoint string
	var timeout time.Duration

	flag.StringVar(&endpoint, "endpoint", "http://localhost:23413/mcp", "MCP streamable HTTP endpoint")
	flag.DurationVar(&timeout, "timeout", 15*time.Second, "overall timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-smoke",
		Version: "v1.0.0",
	}, nil)

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		exitf("connect to %s: %v", endpoint, err)
	}
	defer session.Close()

	if err := runSmoke(ctx, session); err != nil {
		exitf("smoke failed: %v", err)
	}

	fmt.Println("MCP smoke check passed")
}

const toolPluginsList = "plugins_list"

func runSmoke(ctx context.Context, session *mcp.ClientSession) error {
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	if len(tools.Tools) == 0 {
		return errors.New("tools/list returned empty set")
	}

	toolNames := make([]string, 0, len(tools.Tools))
	nameSet := make(map[string]struct{}, len(tools.Tools))
	for _, t := range tools.Tools {
		toolNames = append(toolNames, t.Name)
		nameSet[t.Name] = struct{}{}
	}
	sort.Strings(toolNames)

	if _, ok := nameSet[toolPluginsList]; !ok {
		return fmt.Errorf("missing required tool %q; got: %s", toolPluginsList, strings.Join(toolNames, ", "))
	}

	pluginsRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolPluginsList,
		Arguments: map[string]any{},
	})
	if err != nil {
		return fmt.Errorf("plugins_list call: %w", err)
	}
	if pluginsRes.IsError {
		return fmt.Errorf("plugins_list returned tool error: %s", toolText(pluginsRes))
	}

	var pluginsOut struct {
		Total int `json:"total"`
	}
	if err := decodeStructured(pluginsRes, &pluginsOut); err != nil {
		return fmt.Errorf("plugins_list decode structured output: %w", err)
	}

	return nil
}

func decodeStructured(res *mcp.CallToolResult, dst any) error {
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, dst)
}

func toolText(res *mcp.CallToolResult) string {
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
