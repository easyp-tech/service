package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
)

// fakeStorage is an in-memory core.BinaryStorage used in tests.
type fakeStorage struct {
	mu        sync.RWMutex
	objects   map[string][]byte
	downloads atomic.Int64
	failWith  error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: make(map[string][]byte)}
}

func (f *fakeStorage) Download(_ context.Context, key string, localPath string) error {
	f.downloads.Add(1)

	if f.failWith != nil {
		return f.failWith
	}

	f.mu.RLock()
	data, ok := f.objects[key]
	f.mu.RUnlock()
	if !ok {
		return os.ErrNotExist
	}

	err := os.MkdirAll(filepath.Dir(localPath), 0o755)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	err = os.WriteFile(localPath, data, 0o644)
	if err != nil {
		return fmt.Errorf("os.WriteFile: %w", err)
	}

	return nil
}

func (f *fakeStorage) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	if f.failWith != nil {
		return nil, 0, f.failWith
	}

	f.mu.RLock()
	data, ok := f.objects[key]
	f.mu.RUnlock()
	if !ok {
		return nil, 0, os.ErrNotExist
	}

	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (f *fakeStorage) Exists(_ context.Context, key string) (bool, error) {
	if f.failWith != nil {
		return false, f.failWith
	}

	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.objects[key]

	return ok, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)

	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// buildArchive packs a plugin version directory containing an entrypoint and
// an optional sidecar file, returning the archive bytes.
func buildArchive(t *testing.T, entrypoint string, sidecars map[string]string) []byte {
	t.Helper()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "plugin"), []byte(entrypoint), 0o755))
	for name, content := range sidecars {
		full := filepath.Join(srcDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	var buf bytes.Buffer
	require.NoError(t, plugarchive.PackDir(srcDir, &buf))

	return buf.Bytes()
}

func TestArchiveKey(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "grpc/go/v1.6.2/plugin.tgz", archiveKey("grpc", "go", "v1.6.2"))
}

func TestConfigWithChecksum(t *testing.T) {
	t.Parallel()

	original := json.RawMessage(`{"command":["/plugins/grpc/go/v1.6.2/plugin"],"env":{"HOME":"/tmp"},"custom_field":42}`)

	updated, err := configWithChecksum(original, "abc123")
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(updated, &raw))

	assert.Equal(t, "abc123", raw["sha256"])
	assert.Equal(t, []any{"/plugins/grpc/go/v1.6.2/plugin"}, raw["command"])
	assert.Equal(t, map[string]any{"HOME": "/tmp"}, raw["env"])
	assert.InDelta(t, 42, raw["custom_field"], 0, "unknown fields must be preserved")
}

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	content := []byte("archive-bytes")
	archivePath := filepath.Join(tmpDir, "plugin.tgz")
	require.NoError(t, os.WriteFile(archivePath, content, 0o644))

	t.Run("match", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, verifyChecksum(archivePath, sha256Hex(content)))
	})

	t.Run("mismatch", func(t *testing.T) {
		t.Parallel()
		err := verifyChecksum(archivePath, sha256Hex([]byte("tampered")))
		assert.ErrorIs(t, err, ErrChecksumMismatch)
	})

	t.Run("empty_expected_skips", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, verifyChecksum(archivePath, ""))
	})
}

func TestAttachArchiveChecksum(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	archive := buildArchive(t, "#!/bin/sh\necho hi\n", nil)
	key := "grpc/go/v1.6.2/plugin.tgz"
	req := core.CreatePluginRequest{
		Group:   "grpc",
		Name:    "go",
		Version: "v1.6.2",
		Config:  json.RawMessage(`{"command":["/plugins/grpc/go/v1.6.2/plugin"]}`),
	}

	t.Run("records_checksum_of_stored_archive", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		config, err := r.attachArchiveChecksum(ctx, req)
		require.NoError(t, err)

		var pCfg PluginConfig
		require.NoError(t, json.Unmarshal(config, &pCfg))
		assert.Equal(t, sha256Hex(archive), pCfg.Sha256,
			"checksum must be computed by the service from the stored object")
	})

	t.Run("missing_archive_is_not_uploaded_error", func(t *testing.T) {
		t.Parallel()

		r := &Registry{pluginsDir: t.TempDir(), storage: newFakeStorage()}

		_, err := r.attachArchiveChecksum(ctx, req)
		assert.ErrorIs(t, err, core.ErrBinaryNotUploaded)
	})

	t.Run("storage_failure_is_unavailable", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.failWith = os.ErrDeadlineExceeded
		r := &Registry{pluginsDir: t.TempDir(), storage: store}

		_, err := r.attachArchiveChecksum(ctx, req)
		assert.ErrorIs(t, err, core.ErrStorageUnavailable)
	})
}

func TestEnsureBinary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	key := "grpc/go/v1.6.2/plugin.tgz"
	entrypoint := "#!/bin/sh\nexec ./app/tool\n"
	archive := buildArchive(t, entrypoint, map[string]string{"app/tool": "sidecar-content"})

	newPlugin := func(binPath, checksum string) *plugin {
		return &plugin{
			GroupName: "grpc",
			Name:      "go",
			Version:   "v1.6.2",
			pluginConfig: PluginConfig{
				Command: []string{binPath},
				Sha256:  checksum,
			},
		}
	}

	// binPathIn returns the entrypoint path inside a fresh plugins dir.
	binPathIn := func(t *testing.T) (string, string) {
		t.Helper()
		pluginsDir := t.TempDir()

		return pluginsDir, filepath.Join(pluginsDir, "grpc", "go", "v1.6.2", "plugin")
	}

	t.Run("downloads_verifies_and_unpacks_with_sidecars", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)

		pluginsDir, binPath := binPathIn(t)
		r := &Registry{pluginsDir: pluginsDir, storage: store}

		require.NoError(t, r.ensureBinary(ctx, newPlugin(binPath, sha256Hex(archive))))

		got, err := os.ReadFile(binPath)
		require.NoError(t, err)
		assert.Equal(t, entrypoint, string(got))

		info, err := os.Stat(binPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "entrypoint must be executable")

		sidecar, err := os.ReadFile(filepath.Join(filepath.Dir(binPath), "app", "tool"))
		require.NoError(t, err)
		assert.Equal(t, "sidecar-content", string(sidecar), "sidecars must be materialized too")
	})

	t.Run("checksum_mismatch_leaves_nothing_unpacked", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, buildArchive(t, "#!/bin/sh\nrm -rf /\n", nil))

		pluginsDir, binPath := binPathIn(t)
		r := &Registry{pluginsDir: pluginsDir, storage: store}

		err := r.ensureBinary(ctx, newPlugin(binPath, sha256Hex(archive)))
		require.ErrorIs(t, err, ErrChecksumMismatch)

		_, statErr := os.Stat(binPath)
		assert.True(t, os.IsNotExist(statErr), "tampered archive must not be unpacked")
	})

	t.Run("download_failure_is_storage_unavailable", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.failWith = os.ErrDeadlineExceeded

		pluginsDir, binPath := binPathIn(t)
		r := &Registry{pluginsDir: pluginsDir, storage: store}

		err := r.ensureBinary(ctx, newPlugin(binPath, ""))
		assert.ErrorIs(t, err, core.ErrStorageUnavailable)
	})

	t.Run("existing_entrypoint_skips_download", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)

		pluginsDir, binPath := binPathIn(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(binPath), 0o755))
		require.NoError(t, os.WriteFile(binPath, []byte("already here"), 0o755))

		r := &Registry{pluginsDir: pluginsDir, storage: store}

		require.NoError(t, r.ensureBinary(ctx, newPlugin(binPath, sha256Hex(archive))))
		assert.Equal(t, int64(0), store.downloads.Load())

		got, err := os.ReadFile(binPath)
		require.NoError(t, err)
		assert.Equal(t, "already here", string(got), "local artifact must not be overwritten")
	})

	t.Run("nil_storage_is_noop", func(t *testing.T) {
		t.Parallel()

		_, binPath := binPathIn(t)
		r := &Registry{pluginsDir: t.TempDir(), storage: nil}

		assert.NoError(t, r.ensureBinary(ctx, newPlugin(binPath, "")))
	})

	t.Run("concurrent_requests_share_download", func(t *testing.T) {
		t.Parallel()

		store := newFakeStorage()
		store.put(key, archive)

		pluginsDir, binPath := binPathIn(t)
		r := &Registry{pluginsDir: pluginsDir, storage: store}

		const workers = 16
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for i := range workers {
			wg.Go(func() {
				errs[i] = r.ensureBinary(ctx, newPlugin(binPath, sha256Hex(archive)))
			})
		}
		wg.Wait()

		for i, err := range errs {
			require.NoError(t, err, "worker %d", i)
		}
		assert.Less(t, store.downloads.Load(), int64(workers),
			"singleflight must collapse concurrent downloads")

		got, err := os.ReadFile(binPath)
		require.NoError(t, err)
		assert.Equal(t, entrypoint, string(got))
	})
}

// TestArchivesStageOnThePluginVolume pins where a download lands.
//
// os.CreateTemp("") would put it in the container's writable layer, charged
// against ephemeral-storage while the plugin volume it is about to be unpacked
// onto sits empty. A 355 MB archive with several downloads in flight is enough
// for the kubelet to evict the pod, which reads as a crash rather than as
// overload.
func TestArchivesStageOnThePluginVolume(t *testing.T) {
	t.Parallel()

	key := "grpc/go/v1.6.2/plugin.tgz"
	archive := buildArchive(t, "#!/bin/sh\nexit 0\n", nil)

	store := newFakeStorage()
	store.put(key, archive)

	pluginsDir := t.TempDir()
	r := &Registry{pluginsDir: pluginsDir, storage: store}

	versionDir := filepath.Join(pluginsDir, "grpc", "go", "v1.6.2")
	require.NoError(t, r.fetchAndUnpack(context.Background(), key, versionDir, sha256Hex(archive)))

	staging := filepath.Join(pluginsDir, tmpDirName)
	info, err := os.Stat(staging)
	require.NoError(t, err, "the staging directory must live inside the plugins volume")
	require.True(t, info.IsDir())

	left, err := os.ReadDir(staging)
	require.NoError(t, err)
	assert.Empty(t, left, "a finished download must leave nothing behind")
}

// put seeds the fake with an object.
func (f *fakeStorage) put(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = bytes.Clone(data)
}
