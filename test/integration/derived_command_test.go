//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/core"
)

// TestRegisterWithoutCommand is what `plugins publish` sends: no command, so
// the service has to work out where the entrypoint lives — and the version has
// to generate from there, through the real database and a real process.
func TestRegisterWithoutCommand(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, nil)

	group, name, version := "test", "derived", uniqueVersion()
	buildStubPlugin(t, harness, group, name, version)

	_, err := harness.core.CreatePlugin(t.Context(), core.CreatePluginRequest{
		Group:   group,
		Name:    name,
		Version: version,
		Tags:    []string{},
	})
	require.NoError(t, err)

	parameter := "derived"

	resp, err := harness.core.Generate(t.Context(), core.GenerateCodeRequest{
		PluginName: group + "/" + name + ":" + version,
		Payload:    &pluginpb.CodeGeneratorRequest{Parameter: &parameter},
	})
	require.NoError(t, err)
	require.Len(t, resp.Payload.GetFile(), 1)
	require.Equal(t, "parameter=derived", resp.Payload.GetFile()[0].GetContent())

	// And republishing replaces the config the same way, without a command.
	_, err = harness.core.UpdatePlugin(t.Context(), core.UpdatePluginRequest{
		Group: group, Name: name, Version: version,
		Config:       []byte(`{}`),
		UpdateConfig: true,
	})
	require.NoError(t, err)

	_, err = harness.core.Generate(t.Context(), core.GenerateCodeRequest{
		PluginName: group + "/" + name + ":" + version,
		Payload:    &pluginpb.CodeGeneratorRequest{Parameter: &parameter},
	})
	require.NoError(t, err)
}
