package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryStorage struct {
	mu    sync.RWMutex
	files map[string][]byte
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{
		files: make(map[string][]byte),
	}
}

func (m *memoryStorage) Open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	m.mu.RLock()
	data, ok := m.files[key]
	m.mu.RUnlock()
	if !ok {
		return nil, 0, os.ErrNotExist
	}

	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (m *memoryStorage) Download(ctx context.Context, key string, localPath string) error {
	m.mu.RLock()
	data, ok := m.files[key]
	m.mu.RUnlock()
	if !ok {
		return os.ErrNotExist
	}

	dir := filepath.Dir(localPath)
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, "plugin-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	_, err = tmpFile.Write(data)
	if closeErr := tmpFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)

		return err
	}

	return os.Rename(tmpPath, localPath)
}

func (m *memoryStorage) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[key] = bytes.Clone(data)
}

func (m *memoryStorage) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.files[key]

	return ok, nil
}

func (m *memoryStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)

	return nil
}

func TestMemoryStorage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newMemoryStorage()
	tmpDir := t.TempDir()

	content := []byte("binary-content")
	key := "grpc/go/v1.5.1/plugin"

	t.Run("put_and_exists", func(t *testing.T) {
		store.put(key, content)

		exists, err := store.Exists(ctx, key)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("open", func(t *testing.T) {
		reader, size, err := store.Open(ctx, key)
		require.NoError(t, err)
		defer func() { _ = reader.Close() }()

		gotContent, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(content, gotContent))
		assert.Equal(t, int64(len(content)), size)
	})

	t.Run("download", func(t *testing.T) {
		destFile := filepath.Join(tmpDir, "downloaded_plugin")
		err := store.Download(ctx, key, destFile)
		require.NoError(t, err)

		gotContent, err := os.ReadFile(destFile)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(content, gotContent))
	})

	t.Run("delete", func(t *testing.T) {
		err := store.Delete(ctx, key)
		require.NoError(t, err)

		exists, err := store.Exists(ctx, key)
		require.NoError(t, err)
		assert.False(t, exists)
	})
}

// TestConcurrentDownloadSamePath verifies that concurrent downloads into the
// same destination never produce a corrupted file: each writer uses a unique
// temp file and atomically renames it into place.
func TestConcurrentDownloadSamePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newMemoryStorage()
	tmpDir := t.TempDir()

	content := bytes.Repeat([]byte("plugin-payload-"), 4096)
	key := "grpc/go/v1.6.2/plugin"
	store.put(key, content)

	destFile := filepath.Join(tmpDir, "grpc", "go", "v1.6.2", "plugin")

	const workers = 16
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = store.Download(ctx, key, destFile)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d", i)
	}

	gotContent, err := os.ReadFile(destFile)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, gotContent))

	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(destFile), "plugin-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, leftovers, "temp files must not leak")
}

func TestFormatKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		key    string
		want   string
	}{
		{name: "no_prefix", prefix: "", key: "grpc/go/v1.6.2/plugin", want: "grpc/go/v1.6.2/plugin"},
		{name: "with_prefix", prefix: "plugins/", key: "grpc/go/v1.6.2/plugin", want: "plugins/grpc/go/v1.6.2/plugin"},
		{name: "leading_slash_trimmed", prefix: "plugins/", key: "/grpc/go/v1.6.2/plugin", want: "plugins/grpc/go/v1.6.2/plugin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &S3Storage{prefix: tt.prefix}
			assert.Equal(t, tt.want, s.formatKey(tt.key))
		})
	}
}
