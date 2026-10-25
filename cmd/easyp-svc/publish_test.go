package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
	"github.com/easyp-tech/service/sdk"
)

// fakeStore is an in-memory archiveStore.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]core.ObjectInfo
	uploads []string
	statErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]core.ObjectInfo{}}
}

func (f *fakeStore) Stat(_ context.Context, key string) (core.ObjectInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.statErr != nil {
		return core.ObjectInfo{}, false, f.statErr
	}

	info, ok := f.objects[key]

	return info, ok, nil
}

//nolint:wrapcheck // a fake reporting what the filesystem said
func (f *fakeStore) UploadFile(_ context.Context, key, localPath, checksum string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.objects[key] = core.ObjectInfo{Size: info.Size(), Sha256: checksum}
	f.uploads = append(f.uploads, key)

	return nil
}

// registration is one call publish made to the service.
type registration struct {
	method string
	key    string
	config map[string]any
}

// fakeService is an in-memory pluginService. Hooks, when set, answer instead
// of the default behaviour of a v1.2.0 server.
type fakeService struct {
	mu         sync.Mutex
	registered map[string]bool
	calls      []registration
	onCreate   func(key string) error
	onUpdate   func(key string) error
	onPreflt   error
}

func newFakeService(registered ...string) *fakeService {
	f := &fakeService{registered: map[string]bool{}}
	for _, key := range registered {
		f.registered[key] = true
	}

	return f
}

func splitKey(key string) (string, string, string) {
	coords, version, _ := strings.Cut(key, ":")
	group, name, _ := strings.Cut(coords, "/")

	return group, name, version
}

func (f *fakeService) ListPlugins(context.Context, ...sdk.ListOption) ([]*generator.PluginInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*generator.PluginInfo, 0, len(f.registered))

	for key := range f.registered {
		group, name, version := splitKey(key)
		out = append(out, &generator.PluginInfo{Group: group, Name: name, Version: version})
	}

	return out, nil
}

//nolint:wrapcheck // a fake answering with the statuses the service sends
func (f *fakeService) CreatePlugin(
	_ context.Context, group, name, version string, config map[string]any, _ []string,
) (*generator.PluginInfo, error) {
	key := pluginDisplayName(pluginInfo{group, name, version})

	if f.onCreate != nil {
		if err := f.onCreate(key); err != nil {
			return nil, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.registered[key] {
		return nil, status.Error(codes.AlreadyExists, "already exists")
	}

	f.registered[key] = true
	f.calls = append(f.calls, registration{"create", key, config})

	return &generator.PluginInfo{}, nil
}

//nolint:wrapcheck // a fake answering with the statuses the service sends
func (f *fakeService) UpdatePlugin(
	_ context.Context, group, name, version string, config map[string]any, _ []string, paths ...string,
) (*generator.PluginInfo, error) {
	key := pluginDisplayName(pluginInfo{group, name, version})

	if group == "easyp" && name == "publish-preflight" {
		if f.onPreflt != nil {
			return nil, f.onPreflt
		}

		return nil, status.Error(codes.NotFound, "not found")
	}

	if f.onUpdate != nil {
		if err := f.onUpdate(key); err != nil {
			return nil, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.registered[key] {
		return nil, status.Error(codes.NotFound, "not found")
	}

	if len(paths) != 1 || paths[0] != "config" {
		return nil, status.Error(codes.InvalidArgument, "publish must replace the config only")
	}

	f.calls = append(f.calls, registration{"update", key, config})

	return &generator.PluginInfo{}, nil
}

func (f *fakeService) callsFor(key string) []registration {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []registration

	for _, c := range f.calls {
		if c.key == key {
			out = append(out, c)
		}
	}

	return out
}

var (
	plgGo   = pluginInfo{group: "grpc", name: "go", version: "v1.6.2"}
	plgJava = pluginInfo{group: "grpc", name: "java", version: "v1.70.0"}
)

// writeArchives lays out a --archives tree of real, unpackable archives and
// returns it with each archive's checksum.
func writeArchives(t *testing.T, coords ...pluginInfo) (string, map[pluginInfo]string) {
	t.Helper()

	root := t.TempDir()
	sums := map[pluginInfo]string{}

	for _, plg := range coords {
		bundle := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bundle, plugarchive.EntrypointName),
			[]byte("#!/bin/sh\necho "+plg.name+"\n"), binPermissions))

		dir := filepath.Join(root, plg.group, plg.name, plg.version)
		require.NoError(t, os.MkdirAll(dir, dirPermissions))

		archive := filepath.Join(dir, plugarchive.ArchiveName)
		file, err := os.Create(archive)
		require.NoError(t, err)
		require.NoError(t, plugarchive.PackDir(bundle, file))
		require.NoError(t, file.Close())

		sums[plg], err = fileSHA256(archive)
		require.NoError(t, err)
	}

	return root, sums
}

func publishFrom(t *testing.T, archives string, dest destination, service *fakeService, force bool) error {
	t.Helper()

	opts := publishOptions{
		archivesDir:    archives,
		force:          force,
		nonInteractive: true,
		parallel:       1,
		buildParallel:  1,
	}

	return runPluginsPublish(t.Context(), opts, dest, func() (pluginService, func(), error) {
		return service, func() {}, nil
	})
}

func TestPublishUploadsThenRegistersWithTheChecksum(t *testing.T) {
	t.Parallel()

	archives, sums := writeArchives(t, plgGo, plgJava)
	store, service := newFakeStore(), newFakeService()

	require.NoError(t, publishFrom(t, archives, storageDestination{store: store}, service, false))

	for _, plg := range []pluginInfo{plgGo, plgJava} {
		info, ok := store.objects[archiveObjectKey(plg)]
		require.True(t, ok, "%s uploaded", pluginDisplayName(plg))
		assert.Equal(t, sums[plg], info.Sha256, "the checksum travels as object metadata")

		calls := service.callsFor(pluginDisplayName(plg))
		require.Len(t, calls, 1)
		assert.Equal(t, "create", calls[0].method)
		assert.Equal(t, map[string]any{"sha256": sums[plg]}, calls[0].config,
			"no command: the server derives it")
	}

	assert.FileExists(t, filepath.Join(archives, plgGo.group, plgGo.name, plgGo.version, plugarchive.ArchiveName),
		"archives given on disk belong to the caller and are not removed")
}

func TestPublishSkipsWhatIsAlreadyThere(t *testing.T) {
	t.Parallel()

	archives, sums := writeArchives(t, plgGo)
	store, service := newFakeStore(), newFakeService(pluginDisplayName(plgGo))
	store.objects[archiveObjectKey(plgGo)] = core.ObjectInfo{Size: 1, Sha256: sums[plgGo]}

	require.NoError(t, publishFrom(t, archives, storageDestination{store: store}, service, false))

	assert.Empty(t, store.uploads)
	assert.Empty(t, service.callsFor(pluginDisplayName(plgGo)))
}

// TestPublishForceRepairsAStaleChecksum is the stand's 333 versions: archive
// in storage, plugin registered, checksum in the registry out of date. --force
// must fix the registry without sending the archive again.
func TestPublishForceRepairsAStaleChecksum(t *testing.T) {
	t.Parallel()

	archives, sums := writeArchives(t, plgGo)
	store, service := newFakeStore(), newFakeService(pluginDisplayName(plgGo))
	store.objects[archiveObjectKey(plgGo)] = core.ObjectInfo{Size: 1, Sha256: sums[plgGo]}

	require.NoError(t, publishFrom(t, archives, storageDestination{store: store}, service, true))

	assert.Empty(t, store.uploads, "storage already holds this exact archive")

	calls := service.callsFor(pluginDisplayName(plgGo))
	require.Len(t, calls, 1)
	assert.Equal(t, "update", calls[0].method)
	assert.Equal(t, map[string]any{"sha256": sums[plgGo]}, calls[0].config)
}

func TestPublishReplacesADifferentArchive(t *testing.T) {
	t.Parallel()

	archives, sums := writeArchives(t, plgGo)
	store, service := newFakeStore(), newFakeService()
	store.objects[archiveObjectKey(plgGo)] = core.ObjectInfo{Size: 1, Sha256: "0000"}

	require.NoError(t, publishFrom(t, archives, storageDestination{store: store}, service, false))

	assert.Equal(t, []string{archiveObjectKey(plgGo)}, store.uploads)
	assert.Equal(t, sums[plgGo], store.objects[archiveObjectKey(plgGo)].Sha256)
}

// TestPublishLetsTheServiceCheckALegacyObject covers an object pushed before
// checksums were recorded: same size, so the service is asked to compare, and
// only when it says the archive differs is it uploaded.
func TestPublishLetsTheServiceCheckALegacyObject(t *testing.T) {
	t.Parallel()

	for name, differs := range map[string]bool{"same archive": false, "different archive": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			archives, sums := writeArchives(t, plgGo)
			path := filepath.Join(archives, plgGo.group, plgGo.name, plgGo.version, plugarchive.ArchiveName)
			info, err := os.Stat(path)
			require.NoError(t, err)

			store, service := newFakeStore(), newFakeService()
			store.objects[archiveObjectKey(plgGo)] = core.ObjectInfo{Size: info.Size()}

			service.onCreate = func(string) error {
				store.mu.Lock()
				defer store.mu.Unlock()

				if differs && store.objects[archiveObjectKey(plgGo)].Sha256 == "" {
					return status.Error(codes.FailedPrecondition, "plugin archive checksum mismatch")
				}

				return nil
			}

			require.NoError(t, publishFrom(t, archives, storageDestination{store: store}, service, false))

			if differs {
				assert.Equal(t, []string{archiveObjectKey(plgGo)}, store.uploads)
				assert.Equal(t, sums[plgGo], store.objects[archiveObjectKey(plgGo)].Sha256)
			} else {
				assert.Empty(t, store.uploads)
			}

			require.Len(t, service.callsFor(pluginDisplayName(plgGo)), 1)
		})
	}
}

func TestPublishRecognisesAnOldServer(t *testing.T) {
	t.Parallel()

	archives, _ := writeArchives(t, plgGo, plgJava)
	store, service := newFakeStore(), newFakeService()
	service.onCreate = func(string) error {
		return status.Error(codes.InvalidArgument, "invalid plugin configuration: empty command array")
	}

	err := publishFrom(t, archives, storageDestination{store: store}, service, false)
	require.ErrorIs(t, err, ErrServerTooOld)
}

func TestPublishStopsAtTheLicencePluginLimit(t *testing.T) {
	t.Parallel()

	archives, _ := writeArchives(t, plgGo, plgJava)
	store, service := newFakeStore(), newFakeService()
	service.onCreate = func(string) error {
		return status.Error(codes.ResourceExhausted, core.ErrMaxPluginsExceeded.Error()+": current 10, limit 10")
	}

	err := publishFrom(t, archives, storageDestination{store: store}, service, false)
	require.ErrorIs(t, err, ErrPublishFailed)

	// One version found the limit; the other is not uploaded for nothing.
	assert.Len(t, store.uploads, 1)
}

func TestPublishPreflight(t *testing.T) {
	t.Parallel()

	archives, _ := writeArchives(t, plgGo)

	t.Run("a refused token stops the run before any work", func(t *testing.T) {
		t.Parallel()

		store, service := newFakeStore(), newFakeService()
		service.onPreflt = status.Error(codes.Unauthenticated, "invalid token")

		err := publishFrom(t, archives, storageDestination{store: store}, service, false)
		require.ErrorIs(t, err, ErrServiceRefused)
		assert.Empty(t, store.uploads)
	})

	t.Run("unreachable storage stops the run before any work", func(t *testing.T) {
		t.Parallel()

		store, service := newFakeStore(), newFakeService()
		store.statErr = os.ErrDeadlineExceeded

		err := publishFrom(t, archives, storageDestination{store: store}, service, false)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		assert.Empty(t, service.calls)
	})
}

func TestPublishIntoAPluginsDir(t *testing.T) {
	t.Parallel()

	archives, _ := writeArchives(t, plgGo)
	pluginsDir := t.TempDir()
	service := newFakeService()

	require.NoError(t, publishFrom(t, archives, dirDestination{root: pluginsDir}, service, false))

	entrypoint := filepath.Join(pluginsDir, plgGo.group, plgGo.name, plgGo.version, plugarchive.EntrypointName)
	got, err := os.ReadFile(entrypoint)
	require.NoError(t, err)
	assert.Contains(t, string(got), "echo go")

	calls := service.callsFor(pluginDisplayName(plgGo))
	require.Len(t, calls, 1)
	assert.Empty(t, calls[0].config, "without object storage there is no checksum to claim")

	// Published once, it is in place: a second run has nothing to do.
	require.NoError(t, publishFrom(t, archives, dirDestination{root: pluginsDir}, service, false))
	assert.Len(t, service.callsFor(pluginDisplayName(plgGo)), 1)
}

func TestPublishNeedsExactlyOneSource(t *testing.T) {
	t.Parallel()

	_, _, err := publishItems(publishOptions{})
	require.ErrorIs(t, err, ErrPublishSource)

	_, _, err = publishItems(publishOptions{registryPath: "registry", archivesDir: "archives"})
	require.ErrorIs(t, err, ErrPublishSource)
}

// TestPublishRegistersWhatIsAlreadyInStorage is what `register --packed` used
// to do: archives uploaded earlier, a service that has never heard of them.
// A version that would have to be built is not rebuilt or sent again, and no
// checksum is claimed — the service takes the stored archive's.
func TestPublishRegistersWhatIsAlreadyInStorage(t *testing.T) {
	t.Parallel()

	store, service := newFakeStore(), newFakeService()
	store.objects[archiveObjectKey(plgGo)] = core.ObjectInfo{Size: 1}

	pub := &publisher{
		dest:         storageDestination{store: store},
		service:      service,
		tracker:      newBuildTracker(1, false),
		registered:   map[string]bool{},
		buildSlots:   make(chan struct{}, 1),
		deliverSlots: make(chan struct{}, 1),
		abort:        func(error) {},
	}

	// A job that cannot be built: reaching docker would fail the test.
	pub.run(t.Context(), []publishItem{{plg: plgGo, job: &buildJob{pluginDir: "/nonexistent"}}})

	require.Equal(t, 1, pub.tracker.succeeded)
	assert.Empty(t, store.uploads)

	calls := service.callsFor(pluginDisplayName(plgGo))
	require.Len(t, calls, 1)
	assert.Equal(t, "create", calls[0].method)
	assert.Empty(t, calls[0].config)
}

// TestPublishRegistersAPluginsDir is `task run-local`: a plugins_dir holding
// unpacked versions, and a service started on it with an empty registry.
func TestPublishRegistersAPluginsDir(t *testing.T) {
	t.Parallel()

	pluginsDir := writePluginTree(t, plugarchive.EntrypointName, plgGo, plgJava)
	service := newFakeService(pluginDisplayName(plgJava))

	opts := publishOptions{pluginsDir: pluginsDir, nonInteractive: true, parallel: 1, buildParallel: 1}

	err := runPluginsPublish(t.Context(), opts, dirDestination{root: pluginsDir},
		func() (pluginService, func(), error) { return service, func() {}, nil })
	require.NoError(t, err)

	require.Len(t, service.callsFor(pluginDisplayName(plgGo)), 1)
	assert.Empty(t, service.callsFor(pluginDisplayName(plgJava)), "already registered and in place")
}

// TestVerifyKeepsTheArchiveItChecked pins that an archive-only build ships the
// archive the round trip unpacked, rather than packing the bundle twice.
func TestVerifyKeepsTheArchiveItChecked(t *testing.T) {
	t.Parallel()

	job := newBundle(t)
	jobs := asArchiveJobs([]buildJob{job})
	job = jobs[0]

	require.NoError(t, os.MkdirAll(job.workDir, dirPermissions))
	require.NoError(t, os.WriteFile(filepath.Join(job.workDir, plugarchive.EntrypointName),
		[]byte("#!/bin/sh\n"), binPermissions))

	_, err := verifyBundle(t.Context(), job, verifyOptions{smoke: false})
	require.NoError(t, err)

	require.True(t, job.cached(), "the archive is the cached artifact of an archive-only build")

	unpacked := filepath.Join(t.TempDir(), "unpacked")
	require.NoError(t, plugarchive.Unpack(job.archivePath, unpacked))
	assert.FileExists(t, filepath.Join(unpacked, plugarchive.EntrypointName))

	leftovers, err := filepath.Glob(filepath.Join(job.outputDir, "plugin-pack-*.tgz"))
	require.NoError(t, err)
	assert.Empty(t, leftovers, "no temporary archive is left behind")
}

func TestPruneEmptyDirsLeavesFailedBuilds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	published := filepath.Join(root, "grpc", "go")
	require.NoError(t, os.MkdirAll(published, 0o755))

	failed := filepath.Join(root, "bufbuild", "es", "v2.5.1")
	require.NoError(t, os.MkdirAll(failed, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(failed, "build.log"), []byte("boom"), 0o644))

	pruneEmptyDirs(root)

	assert.NoDirExists(t, filepath.Join(root, "grpc"), "an emptied {group}/{name} goes")
	assert.FileExists(t, filepath.Join(failed, "build.log"), "a failed build keeps its log")
	assert.DirExists(t, root, "the work directory itself stays")
}
