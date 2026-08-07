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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/plugarchive"
	"github.com/easyp-tech/service/sdk"
)

const (
	progressBarWidth     = 20
	percentMultiplier    = 100
	separatorLength      = 40
	defaultPluginsPrefix = "/plugins"
	// defaultPluginsScanPath is used when no path argument is given.
	defaultPluginsScanPath = "plugins"
	// pluginEntrypointName is the file a built plugin version directory is
	// required to contain, and the one a scan of such a tree looks for.
	pluginEntrypointName = "plugin"
	// defaultRegisterParallel is how many plugins are registered at once. It
	// sits at the lower of the two tiers' max_concurrent_per_ip so that the
	// default does not spend its time being throttled and retried.
	defaultRegisterParallel = 8
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
	// ErrEmptyPluginsDir is returned when config has an empty registry.plugins_dir.
	ErrEmptyPluginsDir = errors.New("registry.plugins_dir is empty in config")
	// ErrRegisterFailed is returned when one or more plugins failed registration and fail-on-error is set.
	ErrRegisterFailed = errors.New("plugin registration failed")
	// ErrPluginLimitReached is returned when the server refuses a registration
	// because the licence tier's plugin cap is full. Retrying cannot help.
	ErrPluginLimitReached = errors.New("the server's licence tier allows no further plugins")
)

type pluginInfo struct {
	group   string
	name    string
	version string
}

// resolvePluginsPrefix picks the server-side plugins root for CreatePlugin command paths.
// Priority: explicit --plugins-prefix > registry.plugins_dir from --cfg > defaultPluginsPrefix.
func resolvePluginsPrefix(cfgPath string, prefixFlag string, prefixExplicit bool) (string, error) {
	if prefixExplicit {
		return filepath.Clean(prefixFlag), nil
	}

	if cfgPath == "" {
		return defaultPluginsPrefix, nil
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("os.ReadFile: %w", err)
	}

	var cfg config.Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return "", fmt.Errorf("yaml.Unmarshal: %w", err)
	}

	if cfg.Registry.PluginsDir == "" {
		return "", fmt.Errorf("%w: %s", ErrEmptyPluginsDir, cfgPath)
	}

	return filepath.Clean(cfg.Registry.PluginsDir), nil
}

// pluginCommandPath builds the server-side command path for a plugin binary.
func pluginCommandPath(pluginsPrefix string, plg pluginInfo) string {
	return path.Join(filepath.ToSlash(pluginsPrefix), plg.group, plg.name, plg.version, pluginEntrypointName)
}

func pluginDisplayName(plg pluginInfo) string {
	return fmt.Sprintf("%s/%s:%s", plg.group, plg.name, plg.version)
}

func getSpinners() []string {
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}

// registerOptions holds the resolved inputs of the plugins register command.
type registerOptions struct {
	scanPath      string
	addr          string
	filter        string
	pluginsPrefix string
	token         string
	tls           clientTLSOptions
	// packed says scanPath holds archives written by `plugins pack` rather than
	// built plugin directories.
	packed         bool
	parallel       int
	nonInteractive bool
	dryRun         bool
	failOnError    bool
}

func runPluginsRegister(ctx context.Context, opts registerOptions) error {
	plugins, err := scanRegisterSource(opts)
	if err != nil {
		return err
	}

	total := len(plugins)
	if total == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "No plugins found matching the criteria.")

		return nil
	}

	if opts.dryRun {
		runPluginsRegisterDryRun(plugins, opts.pluginsPrefix)

		return nil
	}

	tlsOpt, err := opts.tls.sdkOption()
	if err != nil {
		return err
	}

	// CreatePlugin is a mutating method, so the call needs a token. An empty one
	// is passed through deliberately: the server's rejection names the problem
	// better than a client-side guess would.
	sdkOpts := []sdk.Option{tlsOpt}
	if opts.token != "" {
		sdkOpts = append(sdkOpts, sdk.WithToken(opts.token))
	}

	client, err := sdk.NewClient(opts.addr, sdkOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect to gRPC server at %s: %w", opts.addr, err)
	}
	defer func() { _ = client.Close() }()

	return registerAll(ctx, client, plugins, opts.pluginsPrefix, opts.parallel, opts.nonInteractive, opts.failOnError)
}

// registerAll registers every scanned plugin, reporting progress as it goes.
//
// Registrations run in parallel because each one costs far more on the server
// than on the wire: CreatePlugin carries only metadata, but the service reads
// the whole archive out of object storage to checksum it before it answers. One
// at a time, a catalogue of any size is bounded by that round trip repeated.
//
// The useful ceiling is not here but in the server's rate_limit: concurrency
// beyond max_concurrent_per_ip is refused with ResourceExhausted, which the SDK
// retries — so overshooting does not fail, it quietly slows down.
func registerAll(
	ctx context.Context,
	client *sdk.Client,
	plugins []pluginInfo,
	pluginsPrefix string,
	parallel int,
	nonInteractive bool,
	failOnError bool,
) error {
	isInteractive := !nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))
	tracker := newBatchTracker(len(plugins), isInteractive, batchLabels{
		inProgress: "registering",
		skipReason: "already exists",
		succeeded:  "registered",
	})

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(max(parallel, 1))

	for _, plg := range plugins {
		group.Go(func() error {
			ctxErr := groupCtx.Err()
			if ctxErr != nil {
				return fmt.Errorf("groupCtx.Err: %w", ctxErr)
			}

			isSkipped, errReg := registerSinglePlugin(groupCtx, client, plg, pluginsPrefix)
			tracker.finish(pluginDisplayName(plg), isSkipped, errReg)

			// One plugin failing is counted and the run continues; an abort is
			// the whole batch ending, so only that stops the group.
			if errReg != nil && isContextAbort(groupCtx, errReg) {
				return contextAbortErr(groupCtx, errReg)
			}

			return nil
		})
	}

	waitErr := group.Wait()

	tracker.done()

	total, registered, skipped, failed := tracker.snapshot()

	if waitErr != nil {
		return interruptRegister(isInteractive, total, registered, skipped, failed, waitErr)
	}

	printRegisterSummary(total, registered, skipped, failed)

	return registrationBatchError(failOnError, failed)
}

// registrationBatchError returns ErrRegisterFailed when fail-on-error is set and any plugin failed.
func registrationBatchError(failOnError bool, failed int) error {
	if failOnError && failed > 0 {
		return fmt.Errorf("%w: %d plugin(s) failed registration", ErrRegisterFailed, failed)
	}

	return nil
}

// scanRegisterSource lists the plugins to register, from either a built tree or
// one packed by `plugins pack`.
//
// Registration carries metadata and a command path, never the artifact itself:
// the service streams the archive from storage to checksum it and unpacks it on
// first use. A packed tree therefore names everything registration needs, which
// is what lets a catalogue be registered from a machine that never built it.
func scanRegisterSource(opts registerOptions) ([]pluginInfo, error) {
	if opts.packed {
		return scanPluginTree(opts.scanPath, opts.filter, plugarchive.ArchiveName)
	}

	return scanPlugins(opts.scanPath, opts.filter)
}

func runPluginsRegisterDryRun(plugins []pluginInfo, pluginsPrefix string) {
	_, _ = fmt.Fprintln(os.Stdout, "Dry-run: would register the following plugins")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	for _, plg := range plugins {
		cmdPath := pluginCommandPath(pluginsPrefix, plg)
		_, _ = fmt.Fprintf(os.Stdout, "%-40s  command: %s\n", pluginDisplayName(plg), cmdPath)
	}
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "Would register: %d plugin(s)\n", len(plugins))
}

func isContextAbort(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}

	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func contextAbortErr(ctx context.Context, err error) error {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return fmt.Errorf("ctx.Err: %w", ctxErr)
	}

	return err
}

func interruptRegister(
	isInteractive bool,
	total, registered, skipped, failed int,
	err error,
) error {
	if isInteractive {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Interrupted (%v)\n", err)
	printRegisterSummary(total, registered, skipped, failed)

	return fmt.Errorf("plugins register interrupted: %w", err)
}

func printRegisterSummary(total, registered, skipped, failed int) {
	_, _ = fmt.Fprintln(os.Stdout, "\nRegistration results:")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Plugins found", total)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Registered", registered)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Skipped (already exists)", skipped)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Failed", failed)
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
}

// scanPlugins finds plugin version directories holding an unpacked entrypoint.
func scanPlugins(scanPath string, filter string) ([]pluginInfo, error) {
	return scanPluginTree(scanPath, filter, pluginEntrypointName)
}

// scanPluginTree walks scanPath for {group}/{name}/{version}/{leafName} files.
// leafName is the entrypoint binary in a built tree and the archive in a packed
// one, which is the only thing that differs between the two layouts.
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

func registerSinglePlugin(ctx context.Context, client *sdk.Client, plg pluginInfo, pluginsPrefix string) (bool, error) {
	targetPath := pluginCommandPath(pluginsPrefix, plg)
	configMap := map[string]any{
		"command": []any{targetPath},
	}

	_, registerErr := client.CreatePlugin(ctx, plg.group, plg.name, plg.version, configMap, nil)
	if registerErr == nil {
		return false, nil
	}

	st, ok := status.FromError(registerErr)
	if ok && st.Code() == codes.AlreadyExists {
		return true, nil
	}

	// ResourceExhausted covers three different things: a rate limit, a
	// concurrency limit and a licence tier's plugin cap. Only the first two are
	// worth waiting out — the cap will refuse every retry just as firmly — and
	// the SDK retries all three alike, so an unqualified message here leaves a
	// hard limit looking like a busy server that went quiet for a while.
	if ok && st.Code() == codes.ResourceExhausted &&
		strings.Contains(st.Message(), core.ErrMaxPluginsExceeded.Error()) {
		return false, fmt.Errorf("%w: %s", ErrPluginLimitReached, st.Message())
	}

	return false, fmt.Errorf("sdk.Client.CreatePlugin: %w", registerErr)
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

func renderProgressBar(percent int) string {
	width := progressBarWidth
	filled := int(float64(percent) / 100.0 * float64(width))
	filled = min(filled, width)
	empty := width - filled

	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]"
}
