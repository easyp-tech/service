package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// The behaviour plugins publish relies on: the service derives the command,
// takes the checksum from object metadata, holds a caller to the checksum it
// names, and notices on every replica that an archive was republished.

func TestWithDefaultCommand(t *testing.T) {
	t.Parallel()

	const entrypoint = "/plugins/grpc/go/v1.6.2/plugin"

	commandOf := func(t *testing.T, config json.RawMessage) []string {
		t.Helper()

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(config, &pCfg))

		return pCfg.Command
	}

	for name, config := range map[string]json.RawMessage{
		"absent config":    nil,
		"empty object":     json.RawMessage(`{}`),
		"null":             json.RawMessage(`null`),
		"null command":     json.RawMessage(`{"command":null}`),
		"empty command":    json.RawMessage(`{"command":[]}`),
		"only a checksum":  json.RawMessage(`{"sha256":"abc"}`),
		"only environment": json.RawMessage(`{"env":{"HOME":"/tmp"}}`),
	} {
		t.Run(name+" gets the entrypoint", func(t *testing.T) {
			t.Parallel()

			got, err := withDefaultCommand(config, entrypoint)
			require.NoError(t, err)
			assert.Equal(t, []string{entrypoint}, commandOf(t, got))
		})
	}

	t.Run("other fields survive", func(t *testing.T) {
		t.Parallel()

		got, err := withDefaultCommand(json.RawMessage(`{"env":{"HOME":"/tmp"},"timeout":"30s"}`), entrypoint)
		require.NoError(t, err)

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(got, &pCfg))
		assert.Equal(t, map[string]string{"HOME": "/tmp"}, pCfg.Env)
		assert.Equal(t, "30s", pCfg.Timeout)
	})

	t.Run("an explicit command wins", func(t *testing.T) {
		t.Parallel()

		explicit := json.RawMessage(`{"command":["/plugins/grpc/go/v1.6.2/plugin","--flag"]}`)

		got, err := withDefaultCommand(explicit, entrypoint)
		require.NoError(t, err)
		assert.Equal(t, []string{"/plugins/grpc/go/v1.6.2/plugin", "--flag"}, commandOf(t, got))
	})

	t.Run("a malformed command is left for ValidateConfig", func(t *testing.T) {
		t.Parallel()

		malformed := json.RawMessage(`{"command":"/plugins/x"}`)

		got, err := withDefaultCommand(malformed, entrypoint)
		require.NoError(t, err)
		assert.JSONEq(t, string(malformed), string(got))
		require.ErrorIs(t, ValidateConfig(got, "/plugins"), core.ErrInvalidConfig)
	})

	t.Run("unparseable JSON is an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := withDefaultCommand(json.RawMessage(`{`), entrypoint)
		require.ErrorIs(t, err, core.ErrInvalidConfig)
	})
}

func TestPrepareConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	archive := buildArchive(t, "#!/bin/sh\n", nil)
	key := "grpc/go/v1.6.2/plugin.tgz"

	t.Run("a bare registration is completed", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)

		pluginsDir := t.TempDir()
		r := &Registry{pluginsDir: pluginsDir, storage: store}

		got, err := r.prepareConfig(ctx, "grpc", "go", "v1.6.2", nil)
		require.NoError(t, err)

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(got, &pCfg))
		assert.Equal(t, []string{filepath.Join(pluginsDir, "grpc", "go", "v1.6.2", "plugin")}, pCfg.Command)
		assert.Equal(t, sha256Hex(archive), pCfg.Sha256)
	})

	t.Run("without storage only the command is derived", func(t *testing.T) {
		t.Parallel()

		pluginsDir := t.TempDir()
		r := &Registry{pluginsDir: pluginsDir}

		got, err := r.prepareConfig(ctx, "grpc", "go", "v1.6.2", json.RawMessage(`{}`))
		require.NoError(t, err)

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(got, &pCfg))
		assert.Equal(t, []string{filepath.Join(pluginsDir, "grpc", "go", "v1.6.2", "plugin")}, pCfg.Command)
		assert.Empty(t, pCfg.Sha256)
	})

	t.Run("an explicit command is still held to plugins_dir", func(t *testing.T) {
		t.Parallel()

		r := &Registry{pluginsDir: t.TempDir()}

		_, err := r.prepareConfig(ctx, "grpc", "go", "v1.6.2", json.RawMessage(`{"command":["/bin/sh"]}`))
		require.ErrorIs(t, err, core.ErrInvalidConfig)
	})
}

func TestAttachArchiveChecksumClaims(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	archive := buildArchive(t, "#!/bin/sh\necho hi\n", nil)
	actual := sha256Hex(archive)
	other := sha256Hex([]byte("some other archive"))
	key := "grpc/go/v1.6.2/plugin.tgz"
	command := `"command":["/plugins/grpc/go/v1.6.2/plugin"]`

	recorded := func(t *testing.T, config json.RawMessage) string {
		t.Helper()

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(config, &pCfg))

		return pCfg.Sha256
	}

	t.Run("metadata spares reading the object", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.putWithChecksum(key, archive, actual)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		got, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2", json.RawMessage(`{`+command+`}`))
		require.NoError(t, err)
		assert.Equal(t, actual, recorded(t, got))
		assert.Equal(t, int64(0), store.opens.Load(), "a HEAD is enough when the uploader recorded the checksum")
	})

	t.Run("malformed metadata falls back to reading", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.putWithChecksum(key, archive, "not-a-checksum")
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		got, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2", json.RawMessage(`{`+command+`}`))
		require.NoError(t, err)
		assert.Equal(t, actual, recorded(t, got))
		assert.Equal(t, int64(1), store.opens.Load())
	})

	t.Run("a matching claim is accepted", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.putWithChecksum(key, archive, actual)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		claim := json.RawMessage(`{` + command + `,"sha256":"` + strings.ToUpper(actual) + `"}`)

		got, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2", claim)
		require.NoError(t, err)
		assert.Equal(t, actual, recorded(t, got), "recorded in canonical lowercase")
	})

	t.Run("a claim against metadata that differs is refused", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.putWithChecksum(key, archive, actual)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		_, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2",
			json.RawMessage(`{`+command+`,"sha256":"`+other+`"}`))
		require.ErrorIs(t, err, core.ErrChecksumMismatch)
		assert.Contains(t, err.Error(), key)
	})

	t.Run("a claim against an object without metadata is checked by reading it", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		_, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2",
			json.RawMessage(`{`+command+`,"sha256":"`+other+`"}`))
		require.ErrorIs(t, err, core.ErrChecksumMismatch)

		got, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2",
			json.RawMessage(`{`+command+`,"sha256":"`+actual+`"}`))
		require.NoError(t, err)
		assert.Equal(t, actual, recorded(t, got))
	})

	t.Run("a claim that is not a checksum is an invalid config", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		_, err := r.attachArchiveChecksum(ctx, "grpc", "go", "v1.6.2",
			json.RawMessage(`{`+command+`,"sha256":"abc"}`))
		require.ErrorIs(t, err, core.ErrInvalidConfig)
		assert.Equal(t, int64(0), store.opens.Load(), "rejected before storage is asked")
	})
}

// TestEnsureBinaryRefetchesRepublished is the multi-replica half of
// republishing: UpdatePlugin lands on one replica, and every other one has to
// notice from the checksum alone that its unpacked copy is out of date.
func TestEnsureBinaryRefetchesRepublished(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := "grpc/go/v1.6.2/plugin.tgz"
	oldArchive := buildArchive(t, "#!/bin/sh\necho old\n", nil)
	newArchive := buildArchive(t, "#!/bin/sh\necho new\n", nil)

	setup := func(t *testing.T) (*Registry, *fakeStorage, string) {
		t.Helper()

		pluginsDir := t.TempDir()
		store := newFakeStorage()
		store.put(key, oldArchive)

		return &Registry{pluginsDir: pluginsDir, storage: store}, store,
			filepath.Join(pluginsDir, "grpc", "go", "v1.6.2", "plugin")
	}

	plug := func(binPath, checksum string) *plugin {
		return &plugin{
			GroupName: "grpc", Name: "go", Version: "v1.6.2",
			pluginConfig: PluginConfig{Command: []string{binPath}, Sha256: checksum},
		}
	}

	entrypoint := func(t *testing.T, binPath string) string {
		t.Helper()

		got, err := os.ReadFile(binPath)
		require.NoError(t, err)

		return string(got)
	}

	t.Run("a download leaves a marker", func(t *testing.T) {
		t.Parallel()

		r, _, binPath := setup(t)
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))

		marker, err := os.ReadFile(filepath.Join(filepath.Dir(binPath), checksumMarkerName))
		require.NoError(t, err)
		assert.Equal(t, sha256Hex(oldArchive), strings.TrimSpace(string(marker)))
	})

	t.Run("a new checksum fetches the new archive", func(t *testing.T) {
		t.Parallel()

		r, store, binPath := setup(t)
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))
		require.Contains(t, entrypoint(t, binPath), "old")

		store.put(key, newArchive)
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(newArchive))))

		assert.Contains(t, entrypoint(t, binPath), "new")
		assert.Equal(t, int64(2), store.downloads.Load())
	})

	t.Run("an unchanged checksum is a cache hit", func(t *testing.T) {
		t.Parallel()

		r, store, binPath := setup(t)
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))

		assert.Equal(t, int64(1), store.downloads.Load())
	})

	t.Run("a directory without a marker counts as current", func(t *testing.T) {
		t.Parallel()

		// What every replica holds right after upgrading to v1.2.0: refetching
		// all of it on first use would be a stampede for nothing.
		r, store, binPath := setup(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(binPath), 0o755))
		require.NoError(t, os.WriteFile(binPath, []byte("unpacked by v1.1.0"), 0o755))

		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))

		assert.Equal(t, int64(0), store.downloads.Load())
		assert.Equal(t, "unpacked by v1.1.0", entrypoint(t, binPath))
	})

	t.Run("a stale copy is kept when the new archive does not verify", func(t *testing.T) {
		t.Parallel()

		r, store, binPath := setup(t)
		require.NoError(t, r.ensureBinary(ctx, plug(binPath, sha256Hex(oldArchive))))

		// Registry moved on, storage did not: the download fails verification
		// and nothing replaces what is on disk.
		err := r.ensureBinary(ctx, plug(binPath, sha256Hex(newArchive)))
		require.ErrorIs(t, err, core.ErrChecksumMismatch)
		assert.Contains(t, entrypoint(t, binPath), "old")
		assert.Equal(t, int64(2), store.downloads.Load())
	})
}
