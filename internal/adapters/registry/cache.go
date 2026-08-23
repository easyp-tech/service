package registry

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/monitor"
)

// minEvictionAge is the floor for how recently a plugin may have been used and
// still be evicted. See CacheOptions.MinAge.
const minEvictionAge = 5 * time.Minute

// CacheOptions configures the plugin cache.
type CacheOptions struct {
	// MaxBytes bounds the unpacked plugins on disk. Zero disables eviction.
	MaxBytes int64

	// MinAge protects entries used more recently than this from eviction.
	// Deleting a directory out from under a running plugin process would fail
	// the request in a way that looks like a corrupt artifact, so the floor is
	// derived from the generation timeout: anything older cannot be in use.
	MinAge time.Duration

	Registry  *prometheus.Registry
	Namespace string
}

// cacheEntry is one unpacked plugin version directory.
type cacheEntry struct {
	size     int64
	lastUsed time.Time
}

// pluginCache keeps the unpacked plugin directories under a size limit by
// evicting the least recently used ones.
//
// It only ever removes local files. The archive in object storage stays put, so
// an evicted plugin is downloaded again on its next request; treating the cache
// as the only copy would lose the artifact for good.
//
// Every method tolerates a nil receiver, so callers with eviction disabled — and
// tests building a Registry by hand — need no special case.
type pluginCache struct {
	root   string
	limit  int64
	minAge time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
	total   int64

	sizeGauge prometheus.Gauge
	evictions prometheus.Counter
}

// newPluginCache builds the cache, or returns nil when eviction is disabled.
func newPluginCache(root string, opts CacheOptions) *pluginCache {
	if opts.MaxBytes <= 0 {
		return nil
	}

	minAge := max(opts.MinAge, minEvictionAge)

	sizeGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: opts.Namespace,
		Name:      "plugin_cache_bytes",
		Help:      "Bytes currently occupied by unpacked plugins on local disk.",
	})

	evictions := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: opts.Namespace,
		Name:      "plugin_cache_evictions_total",
		Help:      "Total plugin version directories removed to stay under the cache limit.",
	})

	// Exported so that an alert can ask "how full is the cache" without being
	// told the limit separately. It used to be told: the rule carried the byte
	// count as a literal, copied from the default rather than from the
	// deployment, and on a 14 GB disk it worked out to a threshold of 19.5 GB —
	// a level the filesystem cannot reach, so the alert could never fire while
	// the disk filled underneath it.
	//
	// Set once. The limit is fixed for the life of the process, and a gauge that
	// never moves is still the right shape: it makes the ratio expressible in
	// one query, on both tiers, whatever each was configured with.
	limitGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: opts.Namespace,
		Name:      "plugin_cache_limit_bytes",
		Help:      "Configured ceiling for unpacked plugins on local disk, from registry.cache_max_bytes.",
	})
	limitGauge.Set(float64(opts.MaxBytes))

	if opts.Registry != nil {
		opts.Registry.MustRegister(sizeGauge, evictions, limitGauge)
	}

	return &pluginCache{
		root:      root,
		limit:     opts.MaxBytes,
		minAge:    minAge,
		entries:   make(map[string]cacheEntry),
		sizeGauge: sizeGauge,
		evictions: evictions,
	}
}

// warm populates the index from what is already on disk.
//
// Meant to run in the background: a full cache is tens of thousands of files
// and walking it must not hold up readiness. Until it finishes the cache simply
// accounts for less than is really there, which delays eviction rather than
// breaking it.
func (c *pluginCache) warm(ctx context.Context) {
	if c == nil {
		return
	}

	log := monitor.FromContext(ctx)
	started := time.Now()

	dirs, err := c.versionDirs()
	if err != nil {
		log.Warn("plugin cache scan failed; eviction will undercount until directories are seen again",
			"path", c.root, "error", err)

		return
	}

	now := time.Now()

	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}

		size, sizeErr := dirSize(dir)
		if sizeErr != nil {
			continue
		}

		c.mu.Lock()
		if _, known := c.entries[dir]; !known {
			// Pre-existing directories start out as least recently used: nothing
			// says otherwise, and any that matter will be touched on first use.
			c.entries[dir] = cacheEntry{size: size, lastUsed: now.Add(-c.minAge)}
			c.total += size
		}
		c.mu.Unlock()
	}

	c.mu.Lock()
	total := c.total
	count := len(c.entries)
	c.sizeGauge.Set(float64(total))
	c.mu.Unlock()

	log.Info("plugin cache scanned",
		"directories", count, "bytes", total, "limit", c.limit, "took", time.Since(started))

	c.evict(ctx)
}

// touch records that a plugin was used, keeping it away from the eviction end.
func (c *pluginCache) touch(dir string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, known := c.entries[dir]
	if !known {
		return
	}

	entry.lastUsed = time.Now()
	c.entries[dir] = entry
}

// add measures a freshly unpacked directory, records it, and evicts if the
// cache is now over its limit.
func (c *pluginCache) add(ctx context.Context, dir string) {
	if c == nil {
		return
	}

	size, err := dirSize(dir)
	if err != nil {
		monitor.FromContext(ctx).Warn("failed to measure plugin directory", "path", dir, "error", err)

		return
	}

	c.mu.Lock()
	if previous, known := c.entries[dir]; known {
		c.total -= previous.size
	}

	c.entries[dir] = cacheEntry{size: size, lastUsed: time.Now()}
	c.total += size
	c.sizeGauge.Set(float64(c.total))
	c.mu.Unlock()

	c.evict(ctx)
}

// forget drops a directory from the accounting after someone else removed it.
func (c *pluginCache) forget(dir string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, known := c.entries[dir]
	if !known {
		return
	}

	delete(c.entries, dir)

	c.total -= entry.size
	c.sizeGauge.Set(float64(c.total))
}

// evict removes least recently used directories until the cache fits.
//
// Entries younger than minAge are skipped even when that leaves the cache over
// its limit: overshooting costs disk, whereas deleting a binary that a running
// process is executing costs a failed request and a confusing error.
func (c *pluginCache) evict(ctx context.Context) {
	if c == nil {
		return
	}

	log := monitor.FromContext(ctx)

	for {
		victim, size, ok := c.nextVictim()
		if !ok {
			break
		}

		err := os.RemoveAll(victim)
		if err != nil {
			log.Warn("failed to evict plugin directory", "path", victim, "error", err)
		}

		c.forget(victim)
		c.evictions.Inc()

		log.Info("evicted plugin from cache", "path", victim, "bytes", size)
	}

	if c.overLimit() {
		log.Warn("plugin cache is over its limit but everything in it is too recent to evict",
			"path", c.root, "limit", c.limit, "min_age", c.minAge)
	}
}

// nextVictim picks the oldest evictable entry, or reports that there is none.
func (c *pluginCache) nextVictim() (string, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.total <= c.limit {
		return "", 0, false
	}

	cutoff := time.Now().Add(-c.minAge)

	var (
		oldestDir  string
		oldestTime time.Time
		oldestSize int64
	)

	for dir, entry := range c.entries {
		if entry.lastUsed.After(cutoff) {
			continue
		}

		if oldestDir == "" || entry.lastUsed.Before(oldestTime) {
			oldestDir, oldestTime, oldestSize = dir, entry.lastUsed, entry.size
		}
	}

	if oldestDir == "" {
		// Everything is too recent to touch. Say so once rather than spinning.
		return "", 0, false
	}

	return oldestDir, oldestSize, true
}

// overLimit reports whether the cache exceeds its limit, used for logging the
// case where nothing could be evicted.
func (c *pluginCache) overLimit() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.total > c.limit
}

// versionDirs lists the {group}/{name}/{version} directories under root.
//
// The layout is fixed by archiveKey, so the walk stops at a known depth rather
// than descending into the unpacked payload of every plugin — which is where
// nearly all the files are.
func (c *pluginCache) versionDirs() ([]string, error) {
	const versionDepth = 2 // separators in "group/name/version"

	var dirs []string

	err := filepath.WalkDir(c.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !entry.IsDir() || path == c.root {
			return nil
		}

		// Archives mid-download live here. They are transient and belong to no
		// plugin version, so they are neither cache entries nor candidates for
		// eviction — and descending into them would race the download.
		if entry.Name() == tmpDirName {
			return fs.SkipDir
		}

		rel, relErr := filepath.Rel(c.root, path)
		if relErr != nil {
			return relErr //nolint:wrapcheck // surfaced by the caller with the path attached
		}

		if strings.Count(rel, string(filepath.Separator)) < versionDepth {
			return nil
		}

		dirs = append(dirs, path)

		return fs.SkipDir
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err //nolint:wrapcheck // caller logs it with the path attached
	}

	sort.Strings(dirs)

	return dirs, nil
}

// dirSize sums the apparent size of every regular file under dir.
func dirSize(dir string) (int64, error) {
	var total int64

	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %s: %w", entry.Name(), infoErr)
		}

		total += info.Size()

		return nil
	})
	if err != nil {
		return 0, err //nolint:wrapcheck // caller logs it with the path attached
	}

	return total, nil
}
