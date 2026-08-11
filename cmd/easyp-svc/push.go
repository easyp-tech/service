package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/easyp-tech/service/internal/adapters/storage"
	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/plugarchive"
)

const (
	defaultS3Region = "us-east-1"
	// defaultPushParallel is the number of archives uploaded at once. Object
	// storage tends to cap a single connection far below the available link,
	// so the default is well above one; raise it when the link allows.
	defaultPushParallel = 8
)

var (
	// ErrPushFailed is returned when one or more plugin archives failed to upload.
	ErrPushFailed = errors.New("plugin push failed")
	// ErrS3BucketRequired is returned when no S3 bucket could be resolved.
	ErrS3BucketRequired = errors.New("s3 bucket is required (pass --bucket or configure registry.s3 in --cfg)")
)

// pushOptions holds the resolved inputs of the plugins push command.
type pushOptions struct {
	scanPath string
	filter   string
	s3       storage.S3Options
	// packed says scanPath already holds archives written by `plugins pack`
	// rather than built plugin directories, so nothing has to be packed again.
	packed         bool
	parallel       int
	force          bool
	dryRun         bool
	nonInteractive bool
}

// scanPushSource lists the plugins under scanPath, in whichever of the two
// layouts the command was pointed at.
func scanPushSource(opts pushOptions) ([]pluginInfo, error) {
	if opts.packed {
		return scanPluginTree(opts.scanPath, opts.filter, plugarchive.ArchiveName)
	}

	return scanPlugins(opts.scanPath, opts.filter)
}

// pushSourcePath is the directory or archive a plugin is uploaded from, as
// shown in the dry-run plan.
func pushSourcePath(opts pushOptions, plg pluginInfo) string {
	versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)
	if opts.packed {
		return filepath.Join(versionDir, plugarchive.ArchiveName)
	}

	return versionDir
}

// archiveObjectKey builds the storage object key for a plugin archive.
func archiveObjectKey(plg pluginInfo) string {
	return path.Join(plg.group, plg.name, plg.version, plugarchive.ArchiveName)
}

// resolveS3Options merges S3 settings from CLI flags and the service config.
// Flags win; empty flags fall back to registry.s3 from --cfg.
//
// --cfg expects a whole service configuration, not a fragment holding only
// registry.s3: it goes through the same load and validation the server does, so
// a file missing db.postgres or a port is refused here too. That is deliberate —
// pointing push at a config the server would reject is how the two come to
// disagree about which store they are talking to. To push without one, give the
// storage settings as flags and leave --cfg off.
func resolveS3Options(
	ctx context.Context,
	cfgPath string,
	flagOpts storage.S3Options,
	pathStyleSet bool,
) (storage.S3Options, error) {
	opts := flagOpts

	if cfgPath != "" {
		cfg, _, err := config.LoadAndValidate(ctx, cfgPath)
		if err != nil {
			return storage.S3Options{}, fmt.Errorf("config.LoadAndValidate: %w", err)
		}

		fillFromConfig(&opts, cfg.Registry.S3, pathStyleSet)
	}

	if opts.Region == "" {
		opts.Region = defaultS3Region
	}

	return opts, nil
}

// fillFromConfig fills the fields left empty by CLI flags from registry.s3.
func fillFromConfig(opts *storage.S3Options, s3Cfg config.S3Config, pathStyleSet bool) {
	if opts.Bucket == "" {
		opts.Bucket = s3Cfg.Bucket
	}
	if opts.Endpoint == "" {
		opts.Endpoint = s3Cfg.Endpoint
	}
	if opts.Region == "" {
		opts.Region = s3Cfg.Region
	}
	if opts.Prefix == "" {
		opts.Prefix = s3Cfg.Prefix
	}

	// Credentials travel as a pair: a flag-supplied key must not be silently
	// combined with a config-supplied secret.
	if opts.AccessKeyID == "" && opts.SecretAccessKey == "" {
		opts.AccessKeyID = s3Cfg.AccessKeyID
		opts.SecretAccessKey = s3Cfg.SecretAccessKey
	}

	if !pathStyleSet {
		opts.ForcePathStyle = s3Cfg.ForcePathStyle
	}
}

func runPluginsPush(ctx context.Context, opts pushOptions) error {
	plugins, err := scanPushSource(opts)
	if err != nil {
		return fmt.Errorf("scanPushSource: %w", err)
	}

	total := len(plugins)
	if total == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "No plugins found matching the criteria.")

		return nil
	}

	if opts.dryRun {
		return runPluginsPushDryRun(plugins, opts)
	}

	if opts.s3.Bucket == "" {
		return ErrS3BucketRequired
	}

	store, err := storage.NewS3Storage(ctx, opts.s3)
	if err != nil {
		return fmt.Errorf("storage.NewS3Storage: %w", err)
	}

	return pushAll(ctx, store, plugins, opts)
}

// pushAll uploads every plugin archive, reporting progress as it goes.
//
// Uploads run in parallel because object storage commonly rate-limits a single
// connection well below the link it arrives on: with a per-stream ceiling, the
// only way to use the available bandwidth is more streams.
func pushAll(ctx context.Context, store *storage.S3Storage, plugins []pluginInfo, opts pushOptions) error {
	tracker := newBatchTracker(
		len(plugins),
		!opts.nonInteractive && term.IsTerminal(int(os.Stdout.Fd())),
		batchLabels{inProgress: "pushing", skipReason: "already in storage", succeeded: "pushed"},
	)

	// A limit of zero would let errgroup start nothing at all, so anything
	// below one is read as "one upload at a time".
	parallel := max(opts.parallel, 1)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallel)

	for _, plg := range plugins {
		group.Go(func() error {
			// A failed upload must not strand the rest: it is counted and the
			// run continues, so one bad archive does not cost a whole batch.
			ctxErr := groupCtx.Err()
			if ctxErr != nil {
				return fmt.Errorf("groupCtx.Err: %w", ctxErr)
			}

			wasSkipped, pushErr := pushSinglePlugin(groupCtx, store, plg, opts)
			tracker.finish(pluginDisplayName(plg), wasSkipped, pushErr)

			if errors.Is(pushErr, context.Canceled) {
				return pushErr
			}

			return nil
		})
	}

	waitErr := group.Wait()

	tracker.done()

	total, pushed, skipped, failed := tracker.snapshot()
	printPushSummary(total, pushed, skipped, failed)

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return fmt.Errorf("ctx.Err: %w", ctxErr)
	}
	if waitErr != nil {
		return fmt.Errorf("group.Wait: %w", waitErr)
	}

	if failed > 0 {
		return fmt.Errorf("%w: %d plugin(s) failed to push", ErrPushFailed, failed)
	}

	return nil
}

// pushSinglePlugin packs one plugin version directory and uploads it.
// Returns true when the archive already exists and --force was not set.
func pushSinglePlugin(
	ctx context.Context,
	store *storage.S3Storage,
	plg pluginInfo,
	opts pushOptions,
) (bool, error) {
	key := archiveObjectKey(plg)

	if !opts.force {
		exists, err := store.Exists(ctx, key)
		if err != nil {
			return false, fmt.Errorf("store.Exists: %w", err)
		}
		if exists {
			return true, nil
		}
	}

	archivePath, cleanup, err := resolveArchive(opts, plg)
	if err != nil {
		return false, err
	}
	defer cleanup()

	err = store.UploadFile(ctx, key, archivePath)
	if err != nil {
		return false, fmt.Errorf("store.UploadFile: %w", err)
	}

	return false, nil
}

// resolveArchive returns the archive to upload for one plugin version and a
// cleanup to run once it has been. A packed tree is uploaded from disk and its
// archive is left in place; a built tree is packed into a temporary file that
// the cleanup removes.
func resolveArchive(opts pushOptions, plg pluginInfo) (string, func(), error) {
	versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)

	if opts.packed {
		return filepath.Join(versionDir, plugarchive.ArchiveName), func() {}, nil
	}

	archivePath, err := packPluginDir(versionDir)
	if err != nil {
		return "", func() {}, err
	}

	return archivePath, func() { _ = os.Remove(archivePath) }, nil
}

// packPluginDir writes a tar.gz of versionDir to a temporary file and returns its path.
// The caller is responsible for removing the file.
func packPluginDir(versionDir string) (string, error) {
	tmpFile, err := os.CreateTemp("", "plugin-push-*.tgz")
	if err != nil {
		return "", fmt.Errorf("os.CreateTemp: %w", err)
	}
	tmpPath := tmpFile.Name()

	packErr := plugarchive.PackDir(versionDir, tmpFile)
	closeErr := tmpFile.Close()

	if packErr != nil {
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("plugarchive.PackDir %s: %w", versionDir, packErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)

		return "", fmt.Errorf("tmpFile.Close: %w", closeErr)
	}

	return tmpPath, nil
}

func runPluginsPushDryRun(plugins []pluginInfo, opts pushOptions) error {
	bucket := opts.s3.Bucket
	if bucket == "" {
		bucket = "<bucket>"
	}

	_, _ = fmt.Fprintln(os.Stdout, "Dry-run: would push the following plugin archives")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	for _, plg := range plugins {
		_, _ = fmt.Fprintf(os.Stdout, "%-40s  %s -> s3://%s/%s%s\n",
			pluginDisplayName(plg), pushSourcePath(opts, plg), bucket, opts.s3.Prefix, archiveObjectKey(plg))
	}
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "Would push: %d plugin(s)\n", len(plugins))

	return nil
}

func printPushSummary(total, pushed, skipped, failed int) {
	_, _ = fmt.Fprintln(os.Stdout, "\nPush results:")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Plugins found", total)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Pushed", pushed)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Skipped (already exists)", skipped)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Failed", failed)
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
}
