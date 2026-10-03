package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/easyp-tech/service/internal/safe"
)

// TestGenerateRunsInPrivateWorkingDirectory pins the working directory a plugin
// gets. It used to inherit the service's, which is / in the image and not
// writable by the service user, and betterproto — which creates its output
// package directories relative to cwd — failed on every request there.
//
// The plugin here does what betterproto does, records where it ran, and the
// test checks that the directory was writable, was not the service's own, and
// is gone once Generate returns.
func TestGenerateRunsInPrivateWorkingDirectory(t *testing.T) {
	t.Parallel()

	pluginsDir := t.TempDir()
	workRoot := filepath.Join(pluginsDir, tmpDirName)
	require.NoError(t, os.MkdirAll(workRoot, dirPerm))

	marker := filepath.Join(t.TempDir(), "cwd")
	script := filepath.Join(pluginsDir, "plugin")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nmkdir -p probe/v1 && pwd > \"$MARKER\"\n"), 0o755))

	plug := &plugin{
		GroupName:     "community",
		Name:          "betterproto",
		Version:       "v1.2.5",
		maxOutputSize: 1 << 20,
		guard:         safe.NewGuard(nil, "test"),
		workRoot:      workRoot,
		pluginConfig: PluginConfig{
			Command: []string{script},
			Env:     map[string]string{"MARKER": marker},
		},
	}

	_, err := plug.Generate(t.Context(), &pluginpb.CodeGeneratorRequest{})
	require.NoError(t, err, "a plugin writing into its working directory must succeed")

	recorded, err := os.ReadFile(marker)
	require.NoError(t, err)

	ranIn := filepath.Clean(string(recorded[:len(recorded)-1]))
	serviceCwd, err := os.Getwd()
	require.NoError(t, err)

	assert.NotEqual(t, serviceCwd, ranIn, "the plugin must not share the service's working directory")
	assert.Contains(t, ranIn, tmpDirName, "the run directory belongs under the plugin volume's staging area")
	assert.NoDirExists(t, ranIn, "the run directory is removed after the plugin exits")

	entries, err := os.ReadDir(workRoot)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is left behind between runs")
}
