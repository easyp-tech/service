package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// TestMCPPluginsList drives the MCP endpoint the way a client does — over
// streamable HTTP, through initialize and tools/call — and pins what the client
// gets back: the build's version, one casing for every field of the result, the
// same JSON in the text and structured content, and a page token that the next
// call accepts under the name the input schema declares.
func TestMCPPluginsList(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	svc := &pagingService{list: core.PluginList{
		Plugins: []core.PluginInfo{{
			ID:        uuid.Must(uuid.NewV4()),
			Group:     "protocolbuffers",
			Name:      "go",
			Version:   "v1.36.11",
			CreatedAt: created,
		}},
		Next: &core.PluginKey{Group: "protocolbuffers", Name: "go", Version: "v1.36.11"},
	}}

	srv := httptest.NewServer(newMCPHandler(svc, nil, "v9.9.9-test"))
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	assert.Equal(t, "v9.9.9-test", session.InitializeResult().ServerInfo.Version)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "plugins_list"})
	require.NoError(t, err)
	require.False(t, result.IsError)

	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.JSONEq(t, string(structured), text.Text, "text and structured content must carry the same JSON")

	// A map rather than a struct: the point is which keys arrive, and a struct
	// would silently drop the ones it does not name.
	var body map[string]any
	require.NoError(t, json.Unmarshal(structured, &body))

	plugins, ok := body["plugins"].([]any)
	require.True(t, ok)
	require.Len(t, plugins, 1)

	plugin, ok := plugins[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, plugin, "createdAt")
	assert.NotContains(t, plugin, "created_at", "every field of the result uses the lowerCamelCase JSON name")
	assert.Equal(t, "2026-11-18T12:00:00Z", plugin["createdAt"])

	nextPageToken, ok := body["nextPageToken"].(string)
	require.True(t, ok)
	require.NotEmpty(t, nextPageToken)

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "plugins_list",
		Arguments: map[string]any{"pageToken": nextPageToken},
	})
	require.NoError(t, err)
	require.NotNil(t, svc.gotPage.After, "the token from one page must reach the core on the next call")
	assert.Equal(t, "v1.36.11", svc.gotPage.After.Version)
}
