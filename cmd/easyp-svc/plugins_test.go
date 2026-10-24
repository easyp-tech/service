package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
)

// writePluginTree lays out {group}/{name}/{version}/{leaf} under a temp dir.
func writePluginTree(t *testing.T, leaf string, coords ...pluginInfo) string {
	t.Helper()

	root := t.TempDir()
	for _, plg := range coords {
		dir := filepath.Join(root, plg.group, plg.name, plg.version)
		require.NoError(t, os.MkdirAll(dir, dirPermissions))
		require.NoError(t, os.WriteFile(filepath.Join(dir, leaf), []byte("payload"), archivePermissions))
	}

	return root
}

func TestScanPluginTree(t *testing.T) {
	t.Parallel()

	plg := pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	other := pluginInfo{group: "protocolbuffers", name: "go", version: "v1.36.0"}

	t.Run("archives", func(t *testing.T) {
		t.Parallel()

		root := writePluginTree(t, plugarchive.ArchiveName, plg)

		got, err := scanPluginTree(root, "", plugarchive.ArchiveName)
		require.NoError(t, err)
		require.Equal(t, []pluginInfo{plg}, got)
	})

	t.Run("the other layout is not mistaken for this one", func(t *testing.T) {
		t.Parallel()

		// An archive tree holds no entrypoints: scanning it for unpacked
		// versions must find nothing rather than half a catalogue.
		root := writePluginTree(t, plugarchive.ArchiveName, plg)

		got, err := scanPluginTree(root, "", pluginBinaryName)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("filter", func(t *testing.T) {
		t.Parallel()

		root := writePluginTree(t, plugarchive.ArchiveName, plg, other)

		got, err := scanPluginTree(root, "grpc/*", plugarchive.ArchiveName)
		require.NoError(t, err)
		require.Equal(t, []pluginInfo{plg}, got)
	})

	t.Run("an archive-only build's scratch directory is not a version", func(t *testing.T) {
		t.Parallel()

		root := writePluginTree(t, plugarchive.ArchiveName, plg)
		bundle := filepath.Join(root, plg.group, plg.name, plg.version, bundleDirName)
		require.NoError(t, os.MkdirAll(bundle, dirPermissions))
		require.NoError(t, os.WriteFile(filepath.Join(bundle, plugarchive.ArchiveName), nil, archivePermissions))

		got, err := scanPluginTree(root, "", plugarchive.ArchiveName)
		require.NoError(t, err)
		require.Equal(t, []pluginInfo{plg}, got)
	})

	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()

		_, err := scanPluginTree(filepath.Join(t.TempDir(), "missing"), "", plugarchive.ArchiveName)
		require.ErrorIs(t, err, ErrDirectoryNotExist)
	})
}

func TestWithThrottleBackoffRetriesThrottleButNotTheCap(t *testing.T) {
	t.Parallel()

	throttle := status.Error(codes.ResourceExhausted, "rejected by grpc_ratelimit")
	ceiling := status.Error(codes.ResourceExhausted, core.ErrMaxPluginsExceeded.Error())

	t.Run("a throttled call succeeds once the server lets it through", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++
			if calls < 4 {
				return fmt.Errorf("attempt %d: %w", calls, throttle)
			}

			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 4, calls)
	})

	t.Run("the plugin cap is not retried", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++

			return fmt.Errorf("attempt %d: %w", calls, ceiling)
		})
		require.ErrorIs(t, err, ceiling)
		require.Equal(t, 1, calls)
	})

	t.Run("a server that never relents is given up on", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := withThrottleBackoff(t.Context(), time.Millisecond, func() error {
			calls++

			return fmt.Errorf("attempt %d: %w", calls, throttle)
		})
		require.ErrorIs(t, err, throttle)
		require.Equal(t, throttleRetries+1, calls)
	})
}
