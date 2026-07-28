package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const (
	defaultBuildParallel  = 4
	dirPermissions        = 0o755
	binPermissions        = 0o755
	logFilePermissions    = 0o644
	stalledReportEvery    = 30 * time.Second
	stalledTickInterval   = 10 * time.Second
	interactiveTickPeriod = 150 * time.Millisecond
	buildLogName          = "build.log"
	pluginConfigName      = "plugin.yaml"
	pluginBinaryName      = "plugin"
	stalledNameWidth      = 50
	maxPanelRows          = 15
)

var (
	// ErrMissingRegistryPath is returned when the registry path argument is missing.
	ErrMissingRegistryPath = errors.New("missing registry path argument; usage: easyp-svc plugins build <registry-path>")
	// ErrDockerNotFound is returned when the docker binary is not available in PATH.
	ErrDockerNotFound = errors.New("docker not found in PATH; install Docker to build plugins")
	// ErrDockerUnavailable is returned when the docker daemon cannot be reached.
	ErrDockerUnavailable = errors.New("docker daemon is not available")
	// ErrNoOutputBinary is returned when a build produced no recognizable binary.
	ErrNoOutputBinary = errors.New("no output binary produced by build")
	// ErrBuildFailed is returned when one or more plugins failed to build.
	ErrBuildFailed = errors.New("one or more plugins failed to build")
)

// pluginConfig mirrors the structure of a registry plugin.yaml file.
//
// Build artifacts are always normalized to the filename "plugin". Dockerfiles
// must COPY the entrypoint to /plugin (optionally with sidecar files such as
// /app or /nodejs). Optional fields: build_args, dockerfile, args.
//
// The legacy "binary" key is accepted for backward compatibility but ignored.
type pluginConfig struct {
	Binary     string            `yaml:"binary"` // deprecated, ignored
	BuildArgs  map[string]string `yaml:"build_args"`
	Dockerfile string            `yaml:"dockerfile"`
	Args       []string          `yaml:"args"`
	Versions   []versionEntry    `yaml:"versions"`
}

// versionEntry accepts both scalar (`- v1.2.3`) and mapping forms. The mapping
// form allows per-version overrides for build_args, dockerfile and args, plus
// a `skip` flag to exclude a single version from builds without removing it
// from the registry.
//
// A mapping entry may set any subset of: version, build_args, dockerfile,
// args, skip. Unset fields inherit the top-level defaults. The legacy "binary"
// key is accepted and ignored.
type versionEntry struct {
	version    string
	buildArgs  map[string]string
	dockerfile string
	args       []string
	skip       bool
}

func (v *versionEntry) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		v.version = node.Value

		return nil
	case yaml.MappingNode:
		var m struct {
			Version    string            `yaml:"version"`
			Binary     string            `yaml:"binary"` // deprecated, ignored
			BuildArgs  map[string]string `yaml:"build_args"`
			Dockerfile string            `yaml:"dockerfile"`
			Args       []string          `yaml:"args"`
			Skip       bool              `yaml:"skip"`
		}
		if err := node.Decode(&m); err != nil {
			return fmt.Errorf("decode version entry: %w", err)
		}
		v.version = m.Version
		v.buildArgs = m.BuildArgs
		v.dockerfile = m.Dockerfile
		v.args = m.Args
		v.skip = m.Skip

		return nil
	default:
		return fmt.Errorf("%w: unexpected version node kind %d", ErrInvalidStructure, node.Kind)
	}
}

// buildJob describes a single plugin version to build (defaults merged with per-version overrides).
type buildJob struct {
	group      string
	name       string
	version    string
	buildArgs  map[string]string
	dockerfile string
	args       []string
	pluginDir  string
	outputDir  string
}

func (j buildJob) key() string {
	return fmt.Sprintf("%s/%s:%s", j.group, j.name, j.version)
}

// cached reports whether the plugin version has already been built.
// The canonical artifact is always a regular file at <outputDir>/plugin;
// a directory of that name means a broken build, not a cached one.
func (j buildJob) cached() bool {
	info, err := os.Stat(filepath.Join(j.outputDir, pluginBinaryName))

	return err == nil && info.Mode().IsRegular()
}

func runPluginsBuild(
	ctx context.Context,
	registryPath string,
	outputDir string,
	filter string,
	parallel int,
	force bool,
	dryRun bool,
	nonInteractive bool,
	keepGoing bool,
) error {
	if err := validateRegistryDir(registryPath); err != nil {
		return err
	}

	jobs, skipped, err := scanBuildJobs(registryPath, outputDir, filter)
	if err != nil {
		return err
	}

	total := len(jobs) + len(skipped)
	if total == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "No plugins found matching the criteria.")

		return nil
	}

	toBuild, cached := splitCached(jobs, force)
	printBuildPlan(registryPath, filter, total, len(toBuild), cached, len(skipped), parallel)

	if dryRun {
		printDryRun(toBuild, skipped)

		return nil
	}
	if len(toBuild) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "Nothing to build, all cached!")
		printSkipped(skipped)

		return nil
	}

	if err := checkDocker(ctx); err != nil {
		return err
	}

	interactive := !nonInteractive && term.IsTerminal(int(os.Stdout.Fd()))
	tracker := newBuildTracker(len(toBuild), interactive)

	stop := startTicker(tracker)
	executeBuilds(ctx, toBuild, parallel, keepGoing, tracker)
	stop()

	printBuildSummary(tracker, total, cached, skipped)

	if tracker.failed > 0 {
		return fmt.Errorf("%w: %d failed", ErrBuildFailed, tracker.failed)
	}

	return nil
}

func validateRegistryDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrDirectoryNotExist, path)
		}

		return fmt.Errorf("failed to access registry directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotADirectory, path)
	}

	return nil
}

func scanBuildJobs(registryDir string, outputDir string, filter string) ([]buildJob, []string, error) {
	cleaned := filepath.Clean(registryDir)

	var (
		jobs    []buildJob
		skipped []string
	)
	err := filepath.WalkDir(cleaned, func(path string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() || dirEntry.Name() != pluginConfigName {
			return nil
		}
		fileJobs, fileSkipped, parseErr := jobsFromConfigFile(path, outputDir, filter)
		if parseErr != nil {
			return parseErr
		}
		jobs = append(jobs, fileJobs...)
		skipped = append(skipped, fileSkipped...)

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scanning registry: %w", err)
	}

	sort.Slice(jobs, func(i, j int) bool { return jobs[i].key() < jobs[j].key() })
	sort.Strings(skipped)

	return jobs, skipped, nil
}

func jobsFromConfigFile(configPath string, outputDir string, filter string) ([]buildJob, []string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", configPath, err)
	}

	cfg, err := parsePluginConfig(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", configPath, err)
	}

	pluginDir := filepath.Dir(configPath)
	name := filepath.Base(pluginDir)
	group := filepath.Base(filepath.Dir(pluginDir))

	return jobsFromConfig(cfg, group, name, pluginDir, outputDir, filter)
}

// parsePluginConfig parses plugin.yaml bytes into a pluginConfig without file I/O.
func parsePluginConfig(data []byte) (pluginConfig, error) {
	var cfg pluginConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return pluginConfig{}, fmt.Errorf("parse plugin.yaml: %w", err)
	}

	return cfg, nil
}

// jobsFromConfig expands a parsed pluginConfig into buildJobs, applying per-version
// overrides on top of top-level defaults and collecting `skip: true` entries separately.
func jobsFromConfig(
	cfg pluginConfig,
	group string,
	name string,
	pluginDir string,
	outputDir string,
	filter string,
) ([]buildJob, []string, error) {
	var (
		jobs    []buildJob
		skipped []string
	)
	for _, v := range cfg.Versions {
		if v.version == "" || !matchFilter(group, name, v.version, filter) {
			continue
		}
		if v.skip {
			skipped = append(skipped, fmt.Sprintf("%s/%s:%s", group, name, v.version))

			continue
		}
		jobs = append(jobs, buildJob{
			group:      group,
			name:       name,
			version:    v.version,
			buildArgs:  mergeBuildArgs(cfg.BuildArgs, v.buildArgs),
			dockerfile: overrideString(cfg.Dockerfile, v.dockerfile),
			args:       mergeArgs(cfg.Args, v.args),
			pluginDir:  pluginDir,
			outputDir:  filepath.Join(outputDir, group, name, v.version),
		})
	}

	return jobs, skipped, nil
}

// overrideString returns the per-version value when set, otherwise the default.
func overrideString(defaultVal string, overrideVal string) string {
	if overrideVal != "" {
		return overrideVal
	}

	return defaultVal
}

// mergeBuildArgs shallow-merges top-level build_args with per-version overrides:
// per-version keys replace top-level keys, the rest are kept. Returns nil when both empty.
func mergeBuildArgs(base map[string]string, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	out := make(map[string]string, len(base)+len(override))
	for k, val := range base {
		out[k] = val
	}
	for k, val := range override {
		out[k] = val
	}

	return out
}

// mergeArgs concatenates top-level args with per-version args.
func mergeArgs(base []string, override []string) []string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	out := make([]string, 0, len(base)+len(override))
	out = append(out, base...)
	out = append(out, override...)

	return out
}

func splitCached(jobs []buildJob, force bool) ([]buildJob, int) {
	if force {
		return jobs, 0
	}

	toBuild := make([]buildJob, 0, len(jobs))
	cached := 0
	for _, j := range jobs {
		if j.cached() {
			cached++

			continue
		}
		toBuild = append(toBuild, j)
	}

	return toBuild, cached
}

func checkDocker(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return ErrDockerNotFound
	}

	cmd := exec.CommandContext(ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %w", ErrDockerUnavailable, err)
	}

	return nil
}

func executeBuilds(ctx context.Context, jobs []buildJob, parallel int, keepGoing bool, tracker *buildTracker) {
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(parallel)

	for _, j := range jobs {
		job := j
		group.Go(func() error {
			return buildOne(ctx, job, keepGoing, tracker)
		})
	}

	_ = group.Wait()
}

func buildOne(ctx context.Context, job buildJob, keepGoing bool, tracker *buildTracker) error {
	start := time.Now()

	if err := os.MkdirAll(job.outputDir, dirPermissions); err != nil {
		tracker.finish(job.key(), false, "mkdir failed", time.Since(start).Round(time.Second))

		return keepOrFail(keepGoing, fmt.Errorf("mkdir %s: %w", job.outputDir, err))
	}

	tracker.start(job.key())

	out, err := runDockerBuild(ctx, job)
	dur := time.Since(start).Round(time.Second)
	if err != nil {
		writeBuildLog(job, out, err)
		tracker.finish(job.key(), false, "see "+filepath.Join(job.outputDir, buildLogName), dur)

		return keepOrFail(keepGoing, fmt.Errorf("build %s: %w", job.key(), err))
	}

	details, normErr := normalizeOutput(job)
	if normErr != nil {
		writeBuildLog(job, out, normErr)
		tracker.finish(job.key(), false, normErr.Error(), dur)

		return keepOrFail(keepGoing, fmt.Errorf("build %s: %w", job.key(), normErr))
	}

	tracker.finish(job.key(), true, details, dur)

	return nil
}

func keepOrFail(keepGoing bool, err error) error {
	if keepGoing {
		return nil
	}

	return err
}

func runDockerBuild(ctx context.Context, job buildJob) ([]byte, error) {
	args := []string{
		"build", "--progress=plain",
		"--build-arg", "VERSION=" + job.version,
	}

	keys := make([]string, 0, len(job.buildArgs))
	for k := range job.buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+job.buildArgs[k])
	}

	// Extra per-version docker build flags (e.g. --network, --target).
	args = append(args, job.args...)

	args = append(args,
		"--output", "type=local,dest="+job.outputDir,
		"-f", dockerfilePath(job),
		job.pluginDir,
	)

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	return out.Bytes(), err
}

// dockerfilePath resolves the Dockerfile to use for a job. A per-version dockerfile
// (relative to the plugin dir) wins over the top-level default; falls back to
// `<pluginDir>/Dockerfile` when unset.
func dockerfilePath(job buildJob) string {
	if job.dockerfile == "" {
		return filepath.Join(job.pluginDir, "Dockerfile")
	}
	if filepath.IsAbs(job.dockerfile) {
		return job.dockerfile
	}

	return filepath.Join(job.pluginDir, job.dockerfile)
}

// normalizeOutput ensures the built artifact is available as <outputDir>/plugin
// and returns a short human-readable description of the result.
// Dockerfiles must emit the entrypoint at /plugin (sidecar files are optional).
func normalizeOutput(job buildJob) (string, error) {
	pluginFile := filepath.Join(job.outputDir, pluginBinaryName)
	info, err := os.Stat(pluginFile)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrNoOutputBinary
	}
	_ = os.Chmod(pluginFile, binPermissions)

	return formatSize(info.Size()), nil
}

func writeBuildLog(job buildJob, output []byte, cause error) {
	logPath := filepath.Join(job.outputDir, buildLogName)
	content := fmt.Sprintf("%s\nError: %v\n", output, cause)
	_ = os.WriteFile(logPath, []byte(content), logFilePermissions)
}

func startTicker(tracker *buildTracker) func() {
	interval := stalledTickInterval
	if tracker.interactive {
		interval = interactiveTickPeriod
	}

	ticker := time.NewTicker(interval)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				tracker.tick()
			case <-done:
				return
			}
		}
	}()

	return func() {
		ticker.Stop()
		close(done)
	}
}

func printBuildPlan(registryPath string, filter string, total int, toBuild int, cached int, skipped int, parallel int) {
	if filter != "" {
		_, _ = fmt.Fprintf(os.Stdout, "Сканирование %s (фильтр %q): найдено %d версий\n", registryPath, filter, total)
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "Сканирование %s: найдено %d версий\n", registryPath, total)
	}
	_, _ = fmt.Fprintf(os.Stdout, "Кэш: %d готово, %d к сборке", cached, toBuild)
	if skipped > 0 {
		_, _ = fmt.Fprintf(os.Stdout, ", %d skip", skipped)
	}
	_, _ = fmt.Fprintf(os.Stdout, " (parallel=%d)\n\n", parallel)
}

func printDryRun(jobs []buildJob, skipped []string) {
	if len(jobs) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "Nothing to build, all cached!")
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "Будет собрано:")
		for _, j := range jobs {
			_, _ = fmt.Fprintf(os.Stdout, "  %s\n", j.key())
		}
	}
	printSkipped(skipped)
}

func printSkipped(skipped []string) {
	if len(skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "Пропущено (declared skip):")
	for _, key := range skipped {
		_, _ = fmt.Fprintf(os.Stdout, "  %s\n", key)
	}
}

func printBuildSummary(t *buildTracker, total int, cached int, skipped []string) {
	if t.interactive {
		t.clear()
	}

	_, _ = fmt.Fprintln(os.Stdout, "\nРезультаты сборки:")
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Всего версий найдено", total)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Собрано", t.succeeded)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Пропущено (кэш)", cached)
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Пропущено (skip)", len(skipped))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s : %d\n", "Ошибка сборки", t.failed)
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("-", separatorLength))
	printSkipped(skipped)
}

func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// buildTracker keeps concurrency-safe state of active and completed builds.
type buildTracker struct {
	mu          sync.Mutex
	active      map[string]time.Time
	total       int
	completed   int
	succeeded   int
	failed      int
	startTime   time.Time
	lastReport  time.Time
	interactive bool
	spinIdx     int
	panelLines  int
}

func newBuildTracker(total int, interactive bool) *buildTracker {
	now := time.Now()

	return &buildTracker{
		active:      make(map[string]time.Time),
		total:       total,
		startTime:   now,
		lastReport:  now,
		interactive: interactive,
	}
}

func (t *buildTracker) start(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[key] = time.Now()

	if t.interactive {
		t.redrawLocked("")

		return
	}

	_, _ = fmt.Fprintf(os.Stdout, "Building %s...\n", key)
}

func (t *buildTracker) finish(key string, success bool, details string, dur time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.active, key)
	t.completed++
	if success {
		t.succeeded++
	} else {
		t.failed++
	}
	t.lastReport = time.Now()

	icon := "✅"
	if !success {
		icon = "❌"
	}
	line := fmt.Sprintf("[%4d/%4d] %s %s (%s, %s)", t.completed, t.total, icon, key, details, dur)

	if t.interactive {
		t.redrawLocked(line)

		return
	}

	_, _ = fmt.Fprintln(os.Stdout, line)
}

func (t *buildTracker) tick() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.interactive {
		t.redrawLocked("")

		return
	}

	t.printStalledLocked()
}

// clear erases the live panel; call once before printing the final summary.
func (t *buildTracker) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearPanelLocked()
}

func (t *buildTracker) clearPanelLocked() {
	if t.panelLines > 0 {
		_, _ = fmt.Fprintf(os.Stdout, "\033[%dA", t.panelLines)
	}
	_, _ = fmt.Fprint(os.Stdout, "\033[J")
	t.panelLines = 0
}

// redrawLocked clears the previous panel, optionally prints a finished line
// above it, then re-renders the panel of currently active builds.
func (t *buildTracker) redrawLocked(finishedLine string) {
	t.clearPanelLocked()

	if finishedLine != "" {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", finishedLine)
	}

	t.panelLines = t.renderPanelLocked()
}

// renderPanelLocked prints the live status panel and returns its line count.
func (t *buildTracker) renderPanelLocked() int {
	if t.completed >= t.total {
		return 0
	}

	spinners := getSpinners()
	spinner := spinners[t.spinIdx%len(spinners)]
	t.spinIdx++

	pct := int(float64(t.completed) / float64(t.total) * percentMultiplier)
	bar := renderProgressBar(pct, progressBarWidth)
	header := fmt.Sprintf(
		"%s %s %d/%d (%d%%) | ✅ %d  ❌ %d  | active: %d",
		spinner, bar, t.completed, t.total, pct, t.succeeded, t.failed, len(t.active),
	)
	_, _ = fmt.Fprintf(os.Stdout, "\033[K%s\n", header)
	lines := 1

	type entry struct {
		name string
		dur  time.Duration
	}
	entries := make([]entry, 0, len(t.active))
	for name, started := range t.active {
		entries = append(entries, entry{name: name, dur: time.Since(started).Round(time.Second)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].dur > entries[j].dur })

	extra := 0
	if len(entries) > maxPanelRows {
		extra = len(entries) - maxPanelRows
		entries = entries[:maxPanelRows]
	}

	for _, e := range entries {
		_, _ = fmt.Fprintf(os.Stdout, "\033[K   %s %-*s (%s)\n", spinner, stalledNameWidth, e.name, e.dur)
		lines++
	}
	if extra > 0 {
		_, _ = fmt.Fprintf(os.Stdout, "\033[K   … +%d more\n", extra)
		lines++
	}

	return lines
}

func (t *buildTracker) printStalledLocked() {
	if len(t.active) == 0 || time.Since(t.lastReport) < stalledReportEvery {
		return
	}

	elapsed := time.Since(t.startTime).Round(time.Second)
	_, _ = fmt.Fprintf(os.Stdout, "  ⏳ [%s] %d/%d done | active: %d\n", elapsed, t.completed, t.total, len(t.active))

	type entry struct {
		name string
		dur  time.Duration
	}
	entries := make([]entry, 0, len(t.active))
	for name, started := range t.active {
		entries = append(entries, entry{name: name, dur: time.Since(started).Round(time.Second)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].dur > entries[j].dur })

	for _, e := range entries {
		_, _ = fmt.Fprintf(os.Stdout, "    %-*s (%s)\n", stalledNameWidth, e.name, e.dur)
	}

	t.lastReport = time.Now()
}
