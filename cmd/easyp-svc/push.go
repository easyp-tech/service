package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/easyp-tech/service/internal/adapters/storage"
	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/plugarchive"
)

const defaultS3Region = "us-east-1"

var (
	// ErrPushFailed is returned when one or more plugin archives failed to upload.
	ErrPushFailed = errors.New("plugin push failed")
	// ErrS3BucketRequired is returned when no S3 bucket could be resolved.
	ErrS3BucketRequired = errors.New("s3 bucket is required (pass --bucket or configure registry.s3 in --cfg)")
)

// pushOptions holds the resolved inputs of the plugins push command.
type pushOptions struct {
	scanPath       string
	filter         string
	s3             storage.S3Options
	force          bool
	dryRun         bool
	nonInteractive bool
}

// archiveObjectKey builds the storage object key for a plugin archive.
func archiveObjectKey(plg pluginInfo) string {
	return path.Join(plg.group, plg.name, plg.version, "plugin.tgz")
}

// resolveS3Options merges S3 settings from CLI flags and the service config.
// Flags win; empty flags fall back to registry.s3 from --cfg.
func resolveS3Options(cfgPath string, flagOpts storage.S3Options, pathStyleSet bool) (storage.S3Options, error) {
	opts := flagOpts

	if cfgPath != "" {
		cfg, _, err := config.LoadAndValidate(cfgPath)
		if err != nil {
			return storage.S3Options{}, fmt.Errorf("config.LoadAndValidate: %w", err)
		}

		s3Cfg := cfg.Registry.S3
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
		if opts.AccessKeyID == "" && opts.SecretAccessKey == "" {
			opts.AccessKeyID = s3Cfg.AccessKeyID
			opts.SecretAccessKey = s3Cfg.SecretAccessKey
		}
		if !pathStyleSet {
			opts.ForcePathStyle = s3Cfg.ForcePathStyle
		}
	}

	if opts.Region == "" {
		opts.Region = defaultS3Region
	}

	return opts, nil
}

func runPluginsPush(ctx context.Context, opts pushOptions) error {
	plugins, err := scanPlugins(opts.scanPath, opts.filter)
	if err != nil {
		return fmt.Errorf("scanPlugins: %w", err)
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

	isInteractive := !opts.nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))

	var pushed, skipped, failed int
	spinners := getSpinners()
	spinIdx := 0

	for idx, plg := range plugins {
		if ctxErr := ctx.Err(); ctxErr != nil {
			printPushSummary(total, pushed, skipped, failed)

			return ctxErr
		}

		pName := pluginDisplayName(plg)

		if isInteractive {
			pct := int(float64(idx) / float64(total) * percentMultiplier)
			_, _ = fmt.Fprintf(
				os.Stdout,
				"\r\033[K%s %s Pushing %s... %d%% (%d/%d)",
				spinners[spinIdx],
				renderProgressBar(pct, progressBarWidth),
				pName,
				pct,
				idx,
				total,
			)
			spinIdx = (spinIdx + 1) % len(spinners)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "Pushing %s...\n", pName)
		}

		wasSkipped, pushErr := pushSinglePlugin(ctx, store, plg, opts)
		switch {
		case pushErr != nil:
			failed++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stderr, "Error pushing %s: %v\n", pName, pushErr)
			}
		case wasSkipped:
			skipped++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stdout, "Skipped (already in storage): %s\n", pName)
			}
		default:
			pushed++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stdout, "Successfully pushed %s\n", pName)
			}
		}
	}

	if isInteractive {
		_, _ = fmt.Fprintf(
			os.Stdout,
			"\r\033[K✓ %s Done! 100%% (%d/%d)\n",
			renderProgressBar(percentMultiplier, progressBarWidth),
			total,
			total,
		)
	}

	printPushSummary(total, pushed, skipped, failed)

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

	versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)

	archivePath, err := packPluginDir(versionDir)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(archivePath) }()

	err = store.UploadFile(ctx, key, archivePath)
	if err != nil {
		return false, fmt.Errorf("store.UploadFile: %w", err)
	}

	return false, nil
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
		versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)
		_, _ = fmt.Fprintf(os.Stdout, "%-40s  %s -> s3://%s/%s%s\n",
			pluginDisplayName(plg), versionDir, bucket, opts.s3.Prefix, archiveObjectKey(plg))
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
