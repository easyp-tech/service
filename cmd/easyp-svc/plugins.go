package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/easyp-tech/service/internal/adapters/storage"
	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
)

// What build and publish share: the {group}/{name}/{version} layout, the
// filter, the storage settings and the pacing of calls to the service.

const (
	progressBarWidth  = 20
	percentMultiplier = 100
	separatorLength   = 40
	defaultS3Region   = "us-east-1"
	// archivePermissions is the mode archives are left with on disk.
	archivePermissions = 0o644

	// throttleRetries bounds how many times one call is retried after the
	// server throttles it. Six doublings from the base is about half a minute,
	// which outlasts any burst the default rate limit refuses.
	throttleRetries   = 6
	throttleRetryBase = 500 * time.Millisecond
)

var (
	// ErrDirectoryNotExist is returned when the target directory does not exist.
	ErrDirectoryNotExist = errors.New("directory does not exist")
	// ErrNotADirectory is returned when the target path is not a directory.
	ErrNotADirectory = errors.New("path is not a directory")
	// ErrPathOutsideBase is returned when the target path is outside the base directory.
	ErrPathOutsideBase = errors.New("path outside base directory")
	// ErrInvalidStructure is returned when the path structure is invalid.
	ErrInvalidStructure = errors.New("does not match expected structure")
	// ErrS3BucketRequired is returned when no S3 bucket could be resolved.
	ErrS3BucketRequired = errors.New("s3 bucket is required (pass --bucket or configure registry.s3 in --cfg)")
	// ErrPluginLimitReached is returned when the server refuses a registration
	// because the licence tier's plugin cap is full. Retrying cannot help.
	ErrPluginLimitReached = errors.New("the server's licence tier allows no further plugins")
)

type pluginInfo struct {
	group   string
	name    string
	version string
}

func pluginDisplayName(plg pluginInfo) string {
	return fmt.Sprintf("%s/%s:%s", plg.group, plg.name, plg.version)
}

// archiveObjectKey builds the storage object key for a plugin archive. The
// service reads the same key; see registry.archiveKey.
func archiveObjectKey(plg pluginInfo) string {
	return path.Join(plg.group, plg.name, plg.version, plugarchive.ArchiveName)
}

func getSpinners() []string {
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}

func renderProgressBar(percent int) string {
	width := progressBarWidth
	filled := int(float64(percent) / 100.0 * float64(width))
	filled = min(filled, width)
	empty := width - filled

	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]"
}

// scanPluginTree walks scanPath for {group}/{name}/{version}/{leafName} files.
// leafName is the entrypoint in an unpacked tree and the archive in one written
// by `plugins build --archive`, which is the only thing that differs between
// the two layouts.
func scanPluginTree(scanPath string, filter string, leafName string) ([]pluginInfo, error) {
	info, err := os.Stat(scanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrDirectoryNotExist, scanPath)
		}

		return nil, fmt.Errorf("failed to access directory %s: %w", scanPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotADirectory, scanPath)
	}

	cleanedPath := filepath.Clean(scanPath)

	var plugins []pluginInfo
	err = filepath.WalkDir(cleanedPath, func(path string, dirEntry os.DirEntry, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		if dirEntry.IsDir() {
			return nil
		}
		if dirEntry.Name() == leafName {
			group, name, version, parseErr := parsePluginPath(cleanedPath, path, leafName)
			if parseErr == nil && matchFilter(group, name, version, filter) {
				plugins = append(plugins, pluginInfo{
					group:   group,
					name:    name,
					version: version,
				})
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning directory: %w", err)
	}

	return plugins, nil
}

// parsePluginPath splits {base}/{group}/{name}/{version}/{leafName} into its
// three plugin coordinates.
func parsePluginPath(basePath, fullPath string, leafName string) (string, string, string, error) {
	rel, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", "", "", ErrPathOutsideBase
	}

	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) != 4 || parts[3] != leafName {
		return "", "", "", ErrInvalidStructure
	}

	return parts[0], parts[1], parts[2], nil
}

func matchFilter(group string, name string, version string, pattern string) bool {
	if pattern == "" {
		return true
	}
	fullName := group + "/" + name
	matched, err := filepath.Match(pattern, fullName)
	if err == nil && matched {
		return true
	}
	fullNameWithVersion := group + "/" + name + ":" + version
	matched, err = filepath.Match(pattern, fullNameWithVersion)

	return err == nil && matched
}

// packPluginDirTo writes a tar.gz of versionDir to a temporary file inside
// destDir and returns its path. The caller is responsible for removing it.
func packPluginDirTo(versionDir, destDir string) (string, error) {
	tmpFile, err := os.CreateTemp(destDir, "plugin-pack-*.tgz")
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

// resolveS3Options merges S3 settings from CLI flags and the service config.
// Flags win; empty flags fall back to registry.s3 from --cfg.
//
// --cfg expects a whole service configuration, not a fragment holding only
// registry.s3: it goes through the same load and validation the server does, so
// a file missing db.postgres or a port is refused here too. That is deliberate —
// pointing publish at a config the server would reject is how the two come to
// disagree about which store they are talking to. To publish without one, give
// the storage settings as flags and leave --cfg off.
func resolveS3Options(
	ctx context.Context,
	cfgPath string,
	flagOpts storage.S3Options,
	pathStyleSet bool,
) (storage.S3Options, error) {
	opts := flagOpts

	if cfgPath != "" {
		res, err := config.Load(ctx, cfgPath)

		// Shown rather than dropped. Publishing with a --cfg the server would
		// refuse is precisely how the CLI and the server come to disagree about
		// which bucket they use.
		reportDiagnostics(os.Stderr, res.Diagnostics)

		if err != nil {
			return storage.S3Options{}, configError(res.Diagnostics, err)
		}

		fillFromConfig(&opts, res.Config.Registry.S3, pathStyleSet)
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

// isPluginCap tells a licence tier's plugin ceiling apart from the two other
// things ResourceExhausted means. The ceiling refuses every retry just as
// firmly; the rate and concurrency limits pass once the caller slows down.
func isPluginCap(st *status.Status) bool {
	return strings.Contains(st.Message(), core.ErrMaxPluginsExceeded.Error())
}

// withThrottleBackoff runs attempt, and runs it again after a growing pause
// each time the server answers ResourceExhausted for a reason that waiting
// cures. The SDK deliberately does not retry that code — overload is a
// refusal the service means — but a catalogue's worth of CreatePlugin calls
// is exactly the caller the default rate limit is sized to slow down, and a
// tool whose job is that batch should pace itself rather than report each
// refusal as a failure.
func withThrottleBackoff(ctx context.Context, base time.Duration, attempt func() error) error {
	delay := base

	for i := 0; ; i++ {
		err := attempt()
		if err == nil || i == throttleRetries {
			return err
		}

		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.ResourceExhausted || isPluginCap(st) {
			return err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting to retry: %w", ctx.Err())
		case <-time.After(delay):
		}

		delay *= 2
	}
}
