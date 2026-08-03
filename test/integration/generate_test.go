//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/core"
)

// TestGenerateEndToEnd is the one no unit test can stand in for: a plugin is
// registered in a real database, located, executed as a real process, and its
// output comes back through the pool.
func TestGenerateEndToEnd(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, nil)

	parameter := "paths=source_relative"
	group, name, version := "test", "stub", uniqueVersion()
	binary := buildStubPlugin(t, harness, group, name, version)
	registerPlugin(t, harness, group, name, version, binary)

	resp, err := harness.core.Generate(t.Context(), core.GenerateCodeRequest{
		PluginName: group + "/" + name + ":" + version,
		Payload:    &pluginpb.CodeGeneratorRequest{Parameter: &parameter},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Payload)
	require.Len(t, resp.Payload.GetFile(), 1)

	// The parameter round-tripped through the process, so this response was
	// produced by the plugin for this request rather than by anything upstream.
	require.Equal(t, "stub.txt", resp.Payload.GetFile()[0].GetName())
	require.Equal(t, "parameter="+parameter, resp.Payload.GetFile()[0].GetContent())
}

func TestGenerateUnknownPlugin(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, nil)

	_, err := harness.core.Generate(t.Context(), core.GenerateCodeRequest{
		PluginName: "test/absent:v9.9.9",
		Payload:    &pluginpb.CodeGeneratorRequest{},
	})
	require.ErrorIs(t, err, core.ErrNotFound)
}

// TestGenerateMissingBinary covers the state a half-finished deployment leaves:
// the plugin is registered, its row is intact, and the file is not there.
func TestGenerateMissingBinary(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, nil)

	group, name, version := "test", "gone", uniqueVersion()
	binary := buildStubPlugin(t, harness, group, name, version)
	registerPlugin(t, harness, group, name, version, binary)

	require.NoError(t, os.Remove(binary))

	_, err := harness.core.Generate(t.Context(), core.GenerateCodeRequest{
		PluginName: group + "/" + name + ":" + version,
		Payload:    &pluginpb.CodeGeneratorRequest{},
	})
	require.Error(t, err)
	require.NotContains(t, strings.ToLower(err.Error()), "panic")
}
