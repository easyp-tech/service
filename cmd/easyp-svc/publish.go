package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
	"github.com/easyp-tech/service/sdk"
)

// publish takes a plugin version from source to usable in one pass: build it,
// verify it, put its archive where the service reads it, register it, and
// clean up. One version at a time, so a catalogue never has to fit on the
// publishing machine's disk, and nothing can be registered before its archive
// is in place — the ordering push and register used to leave to whoever ran
// them.

const (
	// defaultPublishParallel is how many versions upload and register at once.
	// Object storage tends to cap a single connection far below the link, so
	// more streams is more throughput; the service's rate limit paces the rest.
	defaultPublishParallel = 8

	stageQueued      = "waiting for build slot"
	stageBuilding    = "building"
	stageHashing     = "hashing"
	stageWaiting     = "waiting for upload slot"
	stageUploading   = "uploading"
	stageRegistering = "registering"

	// preflightKey is an object nobody uploads, asked for once to prove that
	// storage answers with these credentials before any build starts.
	preflightKey = ".easyp-publish-preflight"
)

var (
	// ErrPublishFailed is returned when one or more versions were not published.
	ErrPublishFailed = errors.New("one or more plugins failed to publish")
	// ErrServerTooOld is returned when the server wants a command in every
	// config, which is what servers before v1.2.0 do.
	ErrServerTooOld = errors.New("the server is older than v1.2.0 and cannot derive plugin commands; upgrade it first")
	// ErrPublishSource is returned when the command is given no source, or two.
	ErrPublishSource = errors.New(
		"give a registry path to build from or --archives, not both; with neither, --plugins-dir is the source",
	)
	// ErrServiceRefused is returned when the service turns the token away; no
	// version can be published until that changes.
	ErrServiceRefused = errors.New("the service refused the write token")
)

// archiveStore is the part of object storage publish uses.
type archiveStore interface {
	Stat(ctx context.Context, key string) (core.ObjectInfo, bool, error)
	UploadFile(ctx context.Context, key, localPath, sha256 string) error
}

// pluginService is the part of the service publish talks to.
type pluginService interface {
	ListPlugins(ctx context.Context, opts ...sdk.ListOption) ([]*generator.PluginInfo, error)
	CreatePlugin(
		ctx context.Context, group, name, version string, config map[string]any, tags []string,
	) (*generator.PluginInfo, error)
	UpdatePlugin(
		ctx context.Context, group, name, version string, config map[string]any, tags []string, paths ...string,
	) (*generator.PluginInfo, error)
}

// publishOptions holds the resolved inputs of the plugins publish command.
type publishOptions struct {
	// registryPath is the plugin registry to build from; archivesDir instead
	// names a tree of {group}/{name}/{version}/plugin.tgz built earlier.
	registryPath string
	archivesDir  string
	// pluginsDir, when set, replaces object storage: archives are unpacked
	// into it, for a service that runs plugins straight from that directory.
	pluginsDir string
	// workDir holds versions while they are built; keep leaves them there.
	workDir        string
	keep           bool
	filter         string
	force          bool
	dryRun         bool
	nonInteractive bool
	buildParallel  int
	parallel       int
	verify         verifyOptions
}

// publishItem is one version on its way through.
type publishItem struct {
	plg pluginInfo
	// job is set for a version built here; archive for one taken from disk;
	// inPlace for one found already unpacked in --plugins-dir, which only needs
	// registering.
	job     *buildJob
	archive string
	inPlace bool
}

func (it publishItem) key() string { return pluginDisplayName(it.plg) }

// destination is where a verified archive is put for the service to read.
type destination interface {
	// present reports, cheaply, whether the version is already in place.
	present(ctx context.Context, plg pluginInfo) (bool, error)
	// deliver puts the archive in place unless the same one already is, and
	// reports whether it wrote anything. force skips the comparison.
	deliver(ctx context.Context, plg pluginInfo, archive, checksum string, force bool) (bool, error)
	// claim is the sha256 registration should insist on, if any.
	claim(checksum string) string
	// describe names the destination of plg in a dry run.
	describe(plg pluginInfo) string
}

// storageDestination delivers to object storage.
type storageDestination struct {
	store  archiveStore
	bucket string
	prefix string
}

func (d storageDestination) present(ctx context.Context, plg pluginInfo) (bool, error) {
	_, exists, err := d.store.Stat(ctx, archiveObjectKey(plg))
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", archiveObjectKey(plg), err)
	}

	return exists, nil
}

// deliver uploads the archive unless storage already holds this one.
//
// An object uploaded before v1.2.0 carries no checksum. When its size matches
// it is left alone and registration is asked to check it: the service reads
// the object from inside its own network, which beats sending it again from
// wherever publish runs. If the service finds it differs, publish uploads after
// all — see publishOne.
func (d storageDestination) deliver(
	ctx context.Context, plg pluginInfo, archive, checksum string, force bool,
) (bool, error) {
	key := archiveObjectKey(plg)

	if !force {
		info, exists, err := d.store.Stat(ctx, key)
		if err != nil {
			return false, fmt.Errorf("stat %s: %w", key, err)
		}

		if exists && sameArchive(info, archive, checksum) {
			return false, nil
		}
	}

	err := d.store.UploadFile(ctx, key, archive, checksum)
	if err != nil {
		return false, fmt.Errorf("upload %s: %w", key, err)
	}

	return true, nil
}

// sameArchive reports whether the stored object is, as far as can be told
// without reading it, the archive at path.
func sameArchive(info core.ObjectInfo, path, checksum string) bool {
	if info.Sha256 != "" {
		return strings.EqualFold(info.Sha256, checksum)
	}

	local, err := os.Stat(path)

	return err == nil && local.Size() == info.Size
}

func (storageDestination) claim(checksum string) string { return checksum }

func (d storageDestination) describe(plg pluginInfo) string {
	return "s3://" + d.bucket + "/" + d.prefix + archiveObjectKey(plg)
}

// dirDestination delivers to a plugins_dir the service reads directly. There
// is no object storage behind it, so the service has no checksum to record.
type dirDestination struct {
	root string
}

func (d dirDestination) versionDir(plg pluginInfo) string {
	return filepath.Join(d.root, plg.group, plg.name, plg.version)
}

func (d dirDestination) present(_ context.Context, plg pluginInfo) (bool, error) {
	info, err := os.Stat(filepath.Join(d.versionDir(plg), plugarchive.EntrypointName))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("os.Stat: %w", err)
	}

	return info.Mode().IsRegular(), nil
}

// deliver unpacks with the code the service would use on a download, which
// replaces the version directory atomically.
func (d dirDestination) deliver(_ context.Context, plg pluginInfo, archive, _ string, _ bool) (bool, error) {
	err := plugarchive.Unpack(archive, d.versionDir(plg))
	if err != nil {
		return false, fmt.Errorf("plugarchive.Unpack: %w", err)
	}

	return true, nil
}

func (dirDestination) claim(string) string { return "" }

func (d dirDestination) describe(plg pluginInfo) string { return d.versionDir(plg) }

// publisher runs one publish.
type publisher struct {
	opts    publishOptions
	dest    destination
	service pluginService
	tracker *buildTracker

	// registered holds the versions the service already knows, by key.
	registered map[string]bool

	buildSlots   chan struct{}
	deliverSlots chan struct{}

	// capped is set once the licence tier refuses a new plugin; versions the
	// service does not know yet are no longer worth building after that.
	capped atomic.Bool

	uploaded, created, updated atomic.Int64

	// abort ends the run on an error that every remaining version would hit.
	abort context.CancelCauseFunc
}

func runPluginsPublish(ctx context.Context, opts publishOptions, dest destination, connect func() (pluginService, func(), error)) error {
	items, skipped, err := publishItems(opts)
	if err != nil {
		return err
	}

	total := len(items) + len(skipped)
	if total == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "No plugins found matching the criteria.")

		return nil
	}

	if opts.dryRun {
		printPublishDryRun(items, skipped, dest)

		return nil
	}

	service, closeService, registered, err := prepare(ctx, opts, dest, connect)
	if err != nil {
		return err
	}
	defer closeService()

	runCtx, abort := context.WithCancelCause(ctx)
	defer abort(nil)

	pub := &publisher{
		opts:         opts,
		dest:         dest,
		service:      service,
		tracker:      newBuildTracker(len(items), !opts.nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))),
		registered:   registered,
		buildSlots:   make(chan struct{}, max(opts.buildParallel, 1)),
		deliverSlots: make(chan struct{}, max(opts.parallel, 1)),
		abort:        abort,
	}

	_, _ = fmt.Fprintf(os.Stdout, "Публикация: %d версий (build-parallel=%d, parallel=%d)\n\n",
		len(items), cap(pub.buildSlots), cap(pub.deliverSlots))

	stop := startTicker(pub.tracker)
	pub.run(runCtx, items)
	stop()

	pub.printSummary(total, skipped)

	return pub.outcome(ctx, runCtx)
}

// outcome is what a finished run returns: the error that stopped it, if one
// did, and otherwise whether every version made it.
func (p *publisher) outcome(ctx, runCtx context.Context) error {
	cause := context.Cause(runCtx)
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return fmt.Errorf("publish stopped: %w", cause)
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return fmt.Errorf("interrupted: %w", ctxErr)
	}

	if p.tracker.failed > 0 {
		return fmt.Errorf("%w: %d failed", ErrPublishFailed, p.tracker.failed)
	}

	return nil
}

// prepare checks what a run needs before it starts: Docker when building, then
// the service and storage, by way of preflight.
//
//nolint:ireturn // pluginService is the seam tests replace
func prepare(
	ctx context.Context, opts publishOptions, dest destination, connect func() (pluginService, func(), error),
) (pluginService, func(), map[string]bool, error) {
	if opts.registryPath != "" {
		err := checkDocker(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	service, closeService, err := connect()
	if err != nil {
		return nil, nil, nil, err
	}

	registered, err := preflight(ctx, dest, service)
	if err != nil {
		closeService()

		return nil, nil, nil, err
	}

	return service, closeService, registered, nil
}

// publishItems lists the versions to publish and the ones plugin.yaml skips.
func publishItems(opts publishOptions) ([]publishItem, []string, error) {
	if opts.registryPath == "" && opts.archivesDir == "" && opts.pluginsDir != "" {
		return inPlaceItems(opts)
	}

	if (opts.registryPath == "") == (opts.archivesDir == "") {
		return nil, nil, ErrPublishSource
	}

	if opts.archivesDir != "" {
		return archiveItems(opts)
	}

	err := validateRegistryDir(opts.registryPath)
	if err != nil {
		return nil, nil, err
	}

	jobs, skipped, err := scanBuildJobs(opts.registryPath, opts.workDir, opts.filter)
	if err != nil {
		return nil, nil, err
	}

	jobs = asArchiveJobs(jobs)
	items := make([]publishItem, 0, len(jobs))

	for i := range jobs {
		items = append(items, publishItem{
			plg: pluginInfo{group: jobs[i].group, name: jobs[i].name, version: jobs[i].version},
			job: &jobs[i],
		})
	}

	return items, skipped, nil
}

// archiveItems lists the archives of an --archives tree.
func archiveItems(opts publishOptions) ([]publishItem, []string, error) {
	plugins, err := scanPluginTree(opts.archivesDir, opts.filter, plugarchive.ArchiveName)
	if err != nil {
		return nil, nil, err
	}

	items := make([]publishItem, 0, len(plugins))
	for _, plg := range plugins {
		items = append(items, publishItem{
			plg:     plg,
			archive: filepath.Join(opts.archivesDir, plg.group, plg.name, plg.version, plugarchive.ArchiveName),
		})
	}

	return items, nil, nil
}

// inPlaceItems lists the versions already unpacked in --plugins-dir: what a
// service running from that directory can be told about without building
// anything.
func inPlaceItems(opts publishOptions) ([]publishItem, []string, error) {
	plugins, err := scanPluginTree(opts.pluginsDir, opts.filter, plugarchive.EntrypointName)
	if err != nil {
		return nil, nil, err
	}

	items := make([]publishItem, 0, len(plugins))
	for _, plg := range plugins {
		items = append(items, publishItem{plg: plg, inPlace: true})
	}

	return items, nil, nil
}

// preflight proves, before anything is built, that storage and the service
// will both accept what publish is about to send them, and returns the versions
// the service already has.
//
// Without it a token typo surfaced as the first registration failing — after
// the first build, which for a JVM plugin is minutes, and then once per
// version for the rest of the run.
func preflight(ctx context.Context, dest destination, service pluginService) (map[string]bool, error) {
	if storageDest, ok := dest.(storageDestination); ok {
		_, _, err := storageDest.store.Stat(ctx, preflightKey)
		if err != nil {
			return nil, fmt.Errorf("object storage is not reachable with these settings: %w", err)
		}
	}

	plugins, err := service.ListPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the service's plugins: %w", err)
	}

	registered := make(map[string]bool, len(plugins))
	for _, p := range plugins {
		registered[pluginDisplayName(pluginInfo{group: p.GetGroup(), name: p.GetName(), version: p.GetVersion()})] = true
	}

	// A tags-only update of a version that cannot exist: the service checks
	// the token before anything else and answers NotFound once it passes. Only
	// the tags path is selected, so no config is validated or written.
	_, err = service.UpdatePlugin(ctx, "easyp", "publish-preflight", "v0.0.0", nil, nil, "tags")

	code := status.Code(err)
	if code == codes.NotFound || code == codes.OK {
		return registered, nil
	}

	if code == codes.Unauthenticated || code == codes.PermissionDenied {
		return nil, fmt.Errorf("%w: %w", ErrServiceRefused, err)
	}

	return nil, fmt.Errorf("checking the write token: %w", err)
}

// run publishes every item, at most buildParallel building and parallel
// delivering at any moment.
func (p *publisher) run(ctx context.Context, items []publishItem) {
	group := errgroup.Group{}
	group.SetLimit(cap(p.buildSlots) + cap(p.deliverSlots))

	for _, item := range items {
		if ctx.Err() != nil {
			break
		}

		group.Go(func() error {
			p.publishOne(ctx, item)

			return nil
		})
	}

	_ = group.Wait()
}

// publishOne takes one version through, reporting the outcome to the tracker.
func (p *publisher) publishOne(ctx context.Context, item publishItem) {
	key := item.key()
	isRegistered := p.registered[key]

	inPlace, done := p.alreadyPublished(ctx, item, isRegistered)
	if done {
		p.tracker.skip(key, "already published")

		return
	}

	if !isRegistered && p.capped.Load() {
		p.tracker.finish(key, false, ErrPluginLimitReached.Error(), 0)

		return
	}

	start := time.Now()
	p.tracker.start(key)

	work := p.publishVersion
	if inPlace {
		// Delivered already, by an earlier run or by hand, and only unknown to
		// the service: registering it is all that is left, and rebuilding it
		// would replace something nobody asked to replace.
		work = p.registerInPlace
	}

	details, err := work(ctx, item, isRegistered)
	dur := time.Since(start).Round(time.Second)

	if err != nil {
		p.noteFatal(err)
		p.tracker.finish(key, false, p.failureDetails(item, err), dur)

		return
	}

	p.tracker.finish(key, true, details, dur)
}

// alreadyPublished decides how much of the work a version still needs: done
// when it is in place and registered, inPlace when it is in place but unknown
// to the service. --force makes neither true for a version publish produces.
func (p *publisher) alreadyPublished(ctx context.Context, item publishItem, isRegistered bool) (bool, bool) {
	inPlace := item.inPlace || (!p.opts.force && p.present(ctx, item.plg))

	if inPlace && isRegistered && !p.opts.force {
		return true, true
	}

	// An archive given on disk is what the caller wants published, and seeing
	// whether storage already holds that one costs a hash, not a build.
	if item.archive != "" {
		return false, false
	}

	return inPlace, false
}

// present reports whether the destination already holds plg. A failure to find
// out counts as absent: the version is then published in full, which is the
// safe direction to be wrong in.
func (p *publisher) present(ctx context.Context, plg pluginInfo) bool {
	present, err := p.dest.present(ctx, plg)

	return err == nil && present
}

// registerInPlace registers a version whose archive is already where the
// service reads it. No checksum is claimed: there is no local archive to take
// one from, and the service records the stored one's.
func (p *publisher) registerInPlace(ctx context.Context, item publishItem, isRegistered bool) (string, error) {
	p.tracker.stage(item.key(), stageWaiting)

	select {
	case p.deliverSlots <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("waiting to register: %w", context.Cause(ctx))
	}
	defer func() { <-p.deliverSlots }()

	p.tracker.stage(item.key(), stageRegistering)

	action, err := p.register(ctx, item.plg, "", isRegistered)
	if err != nil {
		return "", err
	}

	return "already in place, " + action, nil
}

// publishVersion does the work for one version and describes what it did.
func (p *publisher) publishVersion(ctx context.Context, item publishItem, isRegistered bool) (string, error) {
	archive, cleanup, err := p.produce(ctx, item)
	if err != nil {
		return "", err
	}
	defer cleanup()

	p.tracker.stage(item.key(), stageHashing)

	checksum, err := fileSHA256(archive)
	if err != nil {
		return "", err
	}

	p.tracker.stage(item.key(), stageWaiting)

	select {
	case p.deliverSlots <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("waiting to upload: %w", context.Cause(ctx))
	}
	defer func() { <-p.deliverSlots }()

	// Checked again now that the slot is ours: another version may have found
	// the limit while this one waited, and uploading it would be for nothing.
	if !isRegistered && p.capped.Load() {
		return "", ErrPluginLimitReached
	}

	wrote, action, err := p.deliverAndRegister(ctx, item, archive, checksum, isRegistered)
	if err != nil {
		return "", err
	}

	if wrote {
		p.uploaded.Add(1)
	}

	return describeOutcome(archive, wrote, action), nil
}

// deliverAndRegister puts the archive in place and registers it, reporting
// whether anything was written and what registration did.
func (p *publisher) deliverAndRegister(
	ctx context.Context, item publishItem, archive, checksum string, isRegistered bool,
) (bool, string, error) {
	p.tracker.stage(item.key(), stageUploading)

	wrote, err := p.dest.deliver(ctx, item.plg, archive, checksum, false)
	if err != nil {
		return false, "", err
	}

	p.tracker.stage(item.key(), stageRegistering)

	action, err := p.register(ctx, item.plg, p.dest.claim(checksum), isRegistered)
	if wrote || status.Code(err) != codes.FailedPrecondition {
		return wrote, action, err
	}

	// The object storage held was from before checksums were recorded, kept on
	// the strength of its size, and the service read it and found it differs.
	// Now it has to be sent.
	p.tracker.stage(item.key(), stageUploading)

	wrote, err = p.dest.deliver(ctx, item.plg, archive, checksum, true)
	if err != nil {
		return false, "", err
	}

	p.tracker.stage(item.key(), stageRegistering)

	action, err = p.register(ctx, item.plg, p.dest.claim(checksum), isRegistered)

	return wrote, action, err
}

// produce returns the archive of one version and what removes it afterwards.
// A built version is built here, under the build limit; an archive given on
// disk is used where it is and never removed.
func (p *publisher) produce(ctx context.Context, item publishItem) (string, func(), error) {
	if item.job == nil {
		return item.archive, func() {}, nil
	}

	p.tracker.stage(item.key(), stageQueued)

	select {
	case p.buildSlots <- struct{}{}:
	case <-ctx.Done():
		return "", func() {}, fmt.Errorf("waiting to build: %w", context.Cause(ctx))
	}

	p.tracker.stage(item.key(), stageBuilding)

	_, err := buildVerified(ctx, *item.job, p.opts.verify, p.tracker)

	<-p.buildSlots

	if err != nil {
		// The build log stays; it is the only account of what went wrong.
		return "", func() {}, err
	}

	cleanup := func() {
		if !p.opts.keep {
			_ = os.RemoveAll(item.job.outputDir)
		}
	}

	return item.job.archivePath, cleanup, nil
}

// register creates the version, or replaces the config of one the service
// already has. Replacing is what makes a republished archive take effect: the
// service records the new checksum and every replica refetches.
//
// The config publish sends is the whole config, so a hand-edited env or
// timeout on a published version does not survive republishing it.
func (p *publisher) register(ctx context.Context, plg pluginInfo, checksum string, isRegistered bool) (string, error) {
	config := map[string]any{}
	if checksum != "" {
		config["sha256"] = checksum
	}

	update := func() (string, error) {
		err := withThrottleBackoff(ctx, throttleRetryBase, func() error {
			_, err := p.service.UpdatePlugin(ctx, plg.group, plg.name, plg.version, config, nil, "config")
			if err != nil {
				return fmt.Errorf("UpdatePlugin: %w", err)
			}

			return nil
		})
		if err != nil {
			return "", classifyRegisterError(err)
		}

		p.updated.Add(1)

		return "updated", nil
	}

	if isRegistered {
		return update()
	}

	err := withThrottleBackoff(ctx, throttleRetryBase, func() error {
		_, err := p.service.CreatePlugin(ctx, plg.group, plg.name, plg.version, config, nil)
		if err != nil {
			return fmt.Errorf("CreatePlugin: %w", err)
		}

		return nil
	})

	switch {
	case err == nil:
		p.created.Add(1)

		return "created", nil
	case status.Code(err) == codes.AlreadyExists:
		// Registered since the run began, by someone else or an earlier
		// attempt that timed out after the service had already answered.
		return update()
	default:
		return "", classifyRegisterError(err)
	}
}

// classifyRegisterError turns the service's answers that mean more than "this
// version failed" into errors that say so.
func classifyRegisterError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	switch {
	case st.Code() == codes.ResourceExhausted && isPluginCap(st):
		return fmt.Errorf("%w: %s", ErrPluginLimitReached, st.Message())
	case st.Code() == codes.InvalidArgument && strings.Contains(st.Message(), "empty command"):
		return ErrServerTooOld
	case st.Code() == codes.Unauthenticated || st.Code() == codes.PermissionDenied:
		return fmt.Errorf("%w: %w", ErrServiceRefused, err)
	default:
		return err
	}
}

// noteFatal stops the run on errors every remaining version would hit, and
// stops building new plugins once the licence tier is full.
func (p *publisher) noteFatal(err error) {
	switch {
	case errors.Is(err, ErrPluginLimitReached):
		p.capped.Store(true)
	case errors.Is(err, ErrServerTooOld), errors.Is(err, ErrServiceRefused):
		p.abort(err)
	}
}

func (p *publisher) failureDetails(item publishItem, err error) string {
	if item.job != nil {
		return failureDetails(*item.job, err)
	}

	return err.Error()
}

// describeOutcome is the progress line's account of a published version.
func describeOutcome(archive string, wrote bool, action string) string {
	size := ""

	info, err := os.Stat(archive)
	if err == nil {
		size = formatSize(info.Size()) + ", "
	}

	if wrote {
		return size + "uploaded, " + action
	}

	return size + "already in storage, " + action
}

// fileSHA256 returns the hex sha256 of the file at path.
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("os.Open: %w", err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()

	_, err = io.Copy(hasher, file)
	if err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func printPublishDryRun(items []publishItem, skipped []string, dest destination) {
	_, _ = fmt.Fprintln(os.Stdout, "Dry-run: would publish the following versions")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))

	for _, item := range items {
		source := item.archive

		switch {
		case item.job != nil:
			source = "build " + item.job.pluginDir
		case item.inPlace:
			source = "in place"
		}

		_, _ = fmt.Fprintf(os.Stdout, "%-40s  %s -> %s\n", item.key(), source, dest.describe(item.plg))
	}

	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "Would publish: %d version(s)\n", len(items))
	printSkipped(skipped)
}

func (p *publisher) printSummary(total int, skipped []string) {
	if p.tracker.interactive {
		p.tracker.clear()
	}

	_, _ = fmt.Fprintln(os.Stdout, "\nРезультаты публикации:")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Всего версий найдено", total)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Опубликовано", p.tracker.succeeded)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "  из них залито", p.uploaded.Load())
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "  создано", p.created.Load())
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "  обновлено", p.updated.Load())
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Пропущено (на месте)", p.tracker.skipped)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Пропущено (skip)", len(skipped))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Ошибка", p.tracker.failed)
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	printSkipped(skipped)
}
