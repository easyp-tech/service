package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeVersionDir creates {root}/{group}/{name}/{version} holding a file of the
// requested size and returns its path.
func writeVersionDir(t *testing.T, root, group, name, version string, size int) string {
	t.Helper()

	dir := filepath.Join(root, group, name, version)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin"), make([]byte, size), 0o644))

	return dir
}

func TestPluginCacheDisabled(t *testing.T) {
	t.Parallel()

	assert.Nil(t, newPluginCache(t.TempDir(), CacheOptions{MaxBytes: 0}),
		"a zero limit must leave eviction switched off")
}

// TestPluginCacheNilIsSafe pins the contract every call site relies on: a
// Registry built without a cache must not need nil checks around it.
func TestPluginCacheNilIsSafe(t *testing.T) {
	t.Parallel()

	var cache *pluginCache

	assert.NotPanics(t, func() {
		cache.touch("/nowhere")
		cache.add(t.Context(), "/nowhere")
		cache.forget("/nowhere")
		cache.evict(t.Context())
		cache.warm(t.Context())
		assert.False(t, cache.overLimit())
	})
}

// TestPluginCacheEvictsLeastRecentlyUsed checks the ordering: the directory
// nobody has asked for goes first.
func TestPluginCacheEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const size = 1000

	old := writeVersionDir(t, root, "grpc", "go", "v1.0.0", size)
	recent := writeVersionDir(t, root, "grpc", "go", "v2.0.0", size)

	// Room for one directory, and nothing is protected by age.
	cache := newPluginCache(root, CacheOptions{MaxBytes: size + 100, MinAge: 0})
	require.NotNil(t, cache)

	cache.add(t.Context(), old)
	cache.add(t.Context(), recent)

	// Both were just added, so age alone cannot separate them: push one back.
	cache.mu.Lock()
	entry := cache.entries[old]
	entry.lastUsed = time.Now().Add(-time.Hour)
	cache.entries[old] = entry
	cache.mu.Unlock()

	cache.evict(t.Context())

	assert.NoDirExists(t, old, "the least recently used directory should have been evicted")
	assert.DirExists(t, recent, "the recently used directory must survive")
}

// TestPluginCacheProtectsRecentEntries pins the safety rule: a plugin that may
// still be executing is never removed, even when that leaves the cache over its
// limit. Losing a binary under a running process is worse than overshooting.
func TestPluginCacheProtectsRecentEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const size = 1000

	first := writeVersionDir(t, root, "grpc", "go", "v1.0.0", size)
	second := writeVersionDir(t, root, "grpc", "go", "v2.0.0", size)

	cache := newPluginCache(root, CacheOptions{MaxBytes: size / 2, MinAge: time.Hour})
	require.NotNil(t, cache)

	cache.add(t.Context(), first)
	cache.add(t.Context(), second)

	cache.evict(t.Context())

	assert.DirExists(t, first, "an entry younger than MinAge must not be evicted")
	assert.DirExists(t, second, "an entry younger than MinAge must not be evicted")
	assert.True(t, cache.overLimit(), "the cache is expected to overshoot rather than evict live plugins")
}

// TestPluginCacheWarmScansExistingDirs checks that a restart picks up what is
// already on disk instead of starting the accounting from zero.
func TestPluginCacheWarmScansExistingDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const size = 500

	writeVersionDir(t, root, "grpc", "go", "v1.0.0", size)
	writeVersionDir(t, root, "protobuf", "python", "v3.0.0", size)

	// Large limit: nothing should be evicted during the scan.
	cache := newPluginCache(root, CacheOptions{MaxBytes: 1 << 20, MinAge: time.Hour})
	require.NotNil(t, cache)

	cache.warm(t.Context())

	cache.mu.Lock()
	defer cache.mu.Unlock()

	assert.Len(t, cache.entries, 2)
	assert.Equal(t, int64(2*size), cache.total)
}

// TestEvictionLeavesArchiveInStorage pins the invariant that separates eviction
// from deletion: the cache may drop local files, but the artifact in object
// storage must survive, or an evicted plugin is gone for good rather than one
// download away.
func TestEvictionLeavesArchiveInStorage(t *testing.T) {
	t.Parallel()

	const key = "grpc/go/v1.6.2/plugin.tgz"

	archive := buildArchive(t, "#!/bin/sh\nexec ./app/tool\n", map[string]string{"app/tool": "sidecar"})

	store := newFakeStorage()
	store.put(key, archive)

	pluginsDir := t.TempDir()
	binPath := filepath.Join(pluginsDir, "grpc", "go", "v1.6.2", "plugin")

	// A limit of one byte, so whatever lands is immediately over budget. MinAge
	// is floored at minEvictionAge regardless of what is asked for, so the entry
	// has to be aged deliberately below — a fresh one is protected by design.
	repo := &Registry{
		pluginsDir: pluginsDir,
		storage:    store,
		cache:      newPluginCache(pluginsDir, CacheOptions{MaxBytes: 1, MinAge: 0}),
	}

	plug := &plugin{
		GroupName: "grpc",
		Name:      "go",
		Version:   "v1.6.2",
		pluginConfig: PluginConfig{
			Command: []string{binPath},
			Sha256:  sha256Hex(archive),
		},
	}

	require.NoError(t, repo.ensureBinary(t.Context(), plug))
	require.FileExists(t, binPath, "a freshly unpacked plugin must survive: it may be about to run")

	// Age it past the protection window and evict.
	versionDir := filepath.Dir(binPath)
	repo.cache.mu.Lock()
	entry := repo.cache.entries[versionDir]
	entry.lastUsed = time.Now().Add(-time.Hour)
	repo.cache.entries[versionDir] = entry
	repo.cache.mu.Unlock()

	repo.cache.evict(t.Context())

	assert.NoFileExists(t, binPath, "the aged entry should have been evicted")

	stillStored, err := store.Exists(t.Context(), key)
	require.NoError(t, err)
	assert.True(t, stillStored, "eviction must not touch object storage")

	// And the plugin is still usable: it simply comes down again.
	require.NoError(t, repo.ensureBinary(t.Context(), plug))
	assert.Equal(t, int64(2), store.downloads.Load(), "an evicted plugin is re-downloaded on demand")
}

// TestPluginCacheForgetKeepsTotalHonest checks that removing a directory by
// another route (DeletePlugin) does not leave its bytes on the books.
func TestPluginCacheForgetKeepsTotalHonest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := writeVersionDir(t, root, "grpc", "go", "v1.0.0", 800)

	cache := newPluginCache(root, CacheOptions{MaxBytes: 1 << 20, MinAge: time.Hour})
	require.NotNil(t, cache)

	cache.add(t.Context(), dir)

	cache.mu.Lock()
	before := cache.total
	cache.mu.Unlock()
	require.Positive(t, before)

	cache.forget(dir)

	cache.mu.Lock()
	defer cache.mu.Unlock()

	assert.Zero(t, cache.total)
	assert.Empty(t, cache.entries)
}
