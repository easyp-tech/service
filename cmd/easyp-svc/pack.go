package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/easyp-tech/service/internal/plugarchive"
)

// ErrPackFailed is returned when one or more plugin archives failed to pack.
var ErrPackFailed = errors.New("plugin pack failed")

// packOptions holds the resolved inputs of the plugins pack command.
type packOptions struct {
	scanPath       string
	outDir         string
	filter         string
	force          bool
	dryRun         bool
	nonInteractive bool
}

// archiveLocalPath builds the on-disk path of a packed plugin archive. It
// mirrors the S3 object layout so a packed tree can be uploaded as-is later.
func archiveLocalPath(outDir string, plg pluginInfo) string {
	return filepath.Join(outDir, plg.group, plg.name, plg.version, plugarchive.ArchiveName)
}

func runPluginsPack(ctx context.Context, opts packOptions) error {
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
		return runPluginsPackDryRun(plugins, opts)
	}

	isInteractive := !opts.nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))

	var packed, skipped, failed int
	spinners := getSpinners()
	spinIdx := 0

	for idx, plg := range plugins {
		if ctxErr := ctx.Err(); ctxErr != nil {
			printPackSummary(total, packed, skipped, failed)

			return ctxErr
		}

		pName := pluginDisplayName(plg)

		if isInteractive {
			pct := int(float64(idx) / float64(total) * percentMultiplier)
			_, _ = fmt.Fprintf(
				os.Stdout,
				"\r\033[K%s %s Packing %s... %d%% (%d/%d)",
				spinners[spinIdx],
				renderProgressBar(pct, progressBarWidth),
				pName,
				pct,
				idx,
				total,
			)
			spinIdx = (spinIdx + 1) % len(spinners)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "Packing %s...\n", pName)
		}

		wasSkipped, packErr := packSinglePlugin(plg, opts)
		switch {
		case packErr != nil:
			failed++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stderr, "Error packing %s: %v\n", pName, packErr)
			}
		case wasSkipped:
			skipped++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stdout, "Skipped (already packed): %s\n", pName)
			}
		default:
			packed++
			if !isInteractive {
				_, _ = fmt.Fprintf(os.Stdout, "Successfully packed %s\n", pName)
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

	printPackSummary(total, packed, skipped, failed)

	if failed > 0 {
		return fmt.Errorf("%w: %d plugin(s) failed to pack", ErrPackFailed, failed)
	}

	return nil
}

// packSinglePlugin writes one plugin version directory to its archive path.
// Returns true when the archive already exists and --force was not set.
func packSinglePlugin(plg pluginInfo, opts packOptions) (bool, error) {
	destPath := archiveLocalPath(opts.outDir, plg)

	if !opts.force {
		if _, err := os.Stat(destPath); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("os.Stat %s: %w", destPath, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), dirPermissions); err != nil {
		return false, fmt.Errorf("os.MkdirAll: %w", err)
	}

	versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)

	// Pack into a sibling temp file first, so an interrupted run never leaves a
	// truncated archive that a later run would skip as already packed.
	tmpPath, err := packPluginDirTo(versionDir, filepath.Dir(destPath))
	if err != nil {
		return false, err
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)

		return false, fmt.Errorf("os.Rename: %w", err)
	}

	return false, nil
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

func runPluginsPackDryRun(plugins []pluginInfo, opts packOptions) error {
	_, _ = fmt.Fprintln(os.Stdout, "Dry-run: would pack the following plugin archives")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	for _, plg := range plugins {
		versionDir := filepath.Join(opts.scanPath, plg.group, plg.name, plg.version)
		_, _ = fmt.Fprintf(os.Stdout, "%-40s  %s -> %s\n",
			pluginDisplayName(plg), versionDir, archiveLocalPath(opts.outDir, plg))
	}
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "Would pack: %d plugin(s)\n", len(plugins))

	return nil
}

func printPackSummary(total, packed, skipped, failed int) {
	_, _ = fmt.Fprintln(os.Stdout, "\nPack results:")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Plugins found", total)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Packed", packed)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Skipped (already packed)", skipped)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Failed", failed)
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
}
