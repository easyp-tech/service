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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/sdk"
)

const (
	progressBarWidth     = 20
	percentMultiplier    = 100
	separatorLength      = 40
	defaultPluginsPrefix = "/plugins"
	// defaultPluginsScanPath is used when no path argument is given.
	defaultPluginsScanPath = "plugins"
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
	return path.Join(filepath.ToSlash(pluginsPrefix), plg.group, plg.name, plg.version, "plugin")
}

func pluginDisplayName(plg pluginInfo) string {
	return fmt.Sprintf("%s/%s:%s", plg.group, plg.name, plg.version)
}

func getSpinners() []string {
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}

// registerOptions holds the resolved inputs of the plugins register command.
type registerOptions struct {
	scanPath       string
	addr           string
	filter         string
	pluginsPrefix  string
	token          string
	tls            clientTLSOptions
	nonInteractive bool
	dryRun         bool
	failOnError    bool
}

func runPluginsRegister(ctx context.Context, opts registerOptions) error {
	plugins, err := scanPlugins(opts.scanPath, opts.filter)
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

	return registerAll(ctx, client, plugins, opts.pluginsPrefix, opts.nonInteractive, opts.failOnError)
}

// registerAll registers every scanned plugin, reporting progress as it goes.
func registerAll(
	ctx context.Context,
	client *sdk.Client,
	plugins []pluginInfo,
	pluginsPrefix string,
	nonInteractive bool,
	failOnError bool,
) error {
	total := len(plugins)
	isInteractive := !nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))

	var registered, skipped, failed int
	spinners := getSpinners()
	spinIdx := 0

	for idx, plg := range plugins {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return interruptRegister(isInteractive, total, registered, skipped, failed, ctxErr)
		}

		pName := pluginDisplayName(plg)
		spinIdx = reportRegisterProgress(isInteractive, spinners, spinIdx, pName, idx, total)

		isSkipped, errReg := registerSinglePlugin(ctx, client, plg, pluginsPrefix)
		if errReg != nil && isContextAbort(ctx, errReg) {
			return interruptRegister(isInteractive, total, registered, skipped, failed, contextAbortErr(ctx, errReg))
		}
		processRegistrationResult(pName, errReg, isSkipped, isInteractive, &registered, &skipped, &failed)
	}

	if isInteractive {
		progressBar := renderProgressBar(percentMultiplier)
		_, _ = fmt.Fprintf(
			os.Stdout,
			"\r\033[K%s %s Done! 100%% (%d/%d)\n",
			"✓",
			progressBar,
			total,
			total,
		)
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

func processRegistrationResult(
	pName string,
	errReg error,
	isSkipped bool,
	isInteractive bool,
	registered *int,
	skipped *int,
	failed *int,
) {
	if errReg != nil {
		*failed++
		if !isInteractive {
			_, _ = fmt.Fprintf(os.Stderr, "Error registering %s: %v\n", pName, errReg)
		}

		return
	}

	if isSkipped {
		*skipped++
		if !isInteractive {
			_, _ = fmt.Fprintf(os.Stdout, "Skipped (already exists): %s\n", pName)
		}

		return
	}

	*registered++
	if !isInteractive {
		_, _ = fmt.Fprintf(os.Stdout, "Successfully registered %s\n", pName)
	}
}

func scanPlugins(scanPath string, filter string) ([]pluginInfo, error) {
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
		if dirEntry.Name() == "plugin" {
			group, name, version, parseErr := parsePluginPath(cleanedPath, path)
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
	if registerErr != nil {
		if errors.Is(registerErr, context.Canceled) || errors.Is(registerErr, context.DeadlineExceeded) {
			return false, fmt.Errorf("sdk.Client.CreatePlugin: %w", registerErr)
		}

		st, ok := status.FromError(registerErr)
		if ok && st.Code() == codes.AlreadyExists {
			return true, nil
		}
		if ok && (st.Code() == codes.Canceled || st.Code() == codes.DeadlineExceeded) {
			return false, fmt.Errorf("sdk.Client.CreatePlugin: %w", registerErr)
		}

		return false, fmt.Errorf("sdk.Client.CreatePlugin: %w", registerErr)
	}

	return false, nil
}

func parsePluginPath(basePath, fullPath string) (string, string, string, error) {
	rel, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", "", "", ErrPathOutsideBase
	}

	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) != 4 || parts[3] != "plugin" {
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

// reportRegisterProgress renders the progress line and returns the next spinner index.
func reportRegisterProgress(isInteractive bool, spinners []string, spinIdx int, pName string, idx, total int) int {
	if !isInteractive {
		_, _ = fmt.Fprintf(os.Stdout, "Registering %s...\n", pName)

		return spinIdx
	}

	pct := int(float64(idx) / float64(total) * percentMultiplier)
	_, _ = fmt.Fprintf(
		os.Stdout,
		"\r\033[K%s %s Registering %s... %d%% (%d/%d)",
		spinners[spinIdx],
		renderProgressBar(pct),
		pName,
		pct,
		idx,
		total,
	)

	return (spinIdx + 1) % len(spinners)
}
