package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/easyp-tech/service/internal/config"
)

// redactedValue stands in for a secret in printed output. It is deliberately not
// a plausible value: anyone pasting it somewhere will find out immediately.
const redactedValue = `"***"`

// printOptions holds the resolved inputs of the config print command.
type printOptions struct {
	// aliases names the alternative environment variables that supplied a
	// value, keyed by the canonical name. Carried here rather than passed
	// separately because it belongs to the same question every other field
	// here answers: how should this setting be printed.
	aliases map[string]string

	cfgPath string
	// origin annotates each setting with the layer it came from. The question
	// this command exists to answer is usually not "what is the value" but
	// "which of the three places set it".
	origin bool
	// changed prints only what differs from the struct tag defaults.
	changed     bool
	showSecrets bool
}

// runConfigValidate resolves the configuration and reports what is wrong with
// it, without starting anything.
//
// The service already refuses to start on a bad config, but only after it has
// been built, shipped and run somewhere — and only for one config at a time.
// This answers the same question about a file on disk, which is what makes it
// usable from CI and from a deploy script before the deploy.
func runConfigValidate(ctx context.Context, cfgPath string) error {
	var (
		res    config.Result
		err    error
		source string
	)

	if cfgPath == "" {
		res, err = config.LoadFromEnv(ctx)
		source = "Configuration from the environment"
	} else {
		res, err = config.Load(ctx, cfgPath)
		source = cfgPath
	}

	// Printed before the error is returned: a mistyped key is the likeliest
	// explanation for a failure right after someone edited the file, and the
	// diagnostic names the key while the error only says the load failed.
	reportDiagnostics(os.Stderr, res.Diagnostics)

	if err != nil {
		return configError(res.Diagnostics, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "%s is valid.\n", source)
	printOriginSummary(os.Stdout, res.Origins)

	for _, note := range configNotes(res.Config) {
		_, _ = fmt.Fprintf(os.Stdout, "note: %s\n", note)
	}

	// Reported as notes rather than failures. This command is meant to be run in
	// CI and on a laptop, where the server's certificates and licence file
	// legitimately do not exist; the same check refuses the start on the machine
	// that is actually starting.
	for _, diag := range res.Config.CheckFiles() {
		_, _ = fmt.Fprintf(os.Stdout, "note: %s\n", diag)
	}

	return nil
}

// reportDiagnostics prints everything the loader found wrong with the input.
//
// Every caller that resolves a configuration prints these. Two of them used to
// discard them with `_` — `config print`, whose entire job is to say what will
// actually apply, and `plugins push`, which is how the CLI and the server come
// to disagree about which bucket they use.
func reportDiagnostics(dst io.Writer, diags config.Diagnostics) {
	for _, diag := range diags {
		_, _ = fmt.Fprintf(dst, "%s: %s\n", diag.Severity, diag)
	}
}

// configError keeps a rejected configuration from being described twice.
//
// The diagnostics have already been printed one per line, each naming its key,
// its line and its likely correction. Returning them again inside the final
// error re-prints the whole set as one escaped string, which is both longer and
// harder to read than what the operator has already been shown.
func configError(diags config.Diagnostics, err error) error {
	if err == nil {
		return nil
	}

	if diags.HasErrors() {
		return errConfigRejected
	}

	return err
}

var errConfigRejected = errors.New("the configuration was rejected; see the diagnostics above")

// configNotes are the things worth saying about a configuration that is
// nonetheless valid.
//
// Shared by `config validate` and by the startup summary so that both paths say
// them. The S3 note used to be printed only when a file was given, which left it
// unsaid on exactly the environment-only path a Helm deployment takes.
func configNotes(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}

	var notes []string

	// A config whose S3 section is filled but whose credentials are absent is
	// valid and still cannot read a plugin, and the person reading this is the
	// one who can still do something about it.
	if cfg.Registry.S3.Enabled() && cfg.Registry.S3.AccessKeyID == "" {
		notes = append(notes,
			"object storage is configured with no credentials; the default AWS credential chain will be used")
	}

	return notes
}

// logConfigSummary records what the service actually resolved, at Info.
//
// This is the diagnosis that works where the service is running. `config print
// --origin` answers the same questions better, but it is a command on a binary
// the operator has to have, in a shell they have to be able to open, at a moment
// after the thing has already gone wrong; a published image being restarted by
// an orchestrator offers none of the three. The summary costs one record per
// start and removes the class of failure where a setting was configured, ignored
// and never mentioned again.
//
// Info rather than Debug because the chart runs at info: a summary the default
// deployment cannot see is a summary written for the wrong reader. Secrets stay
// redacted — this goes wherever the logs go.
func logConfigSummary(ctx context.Context, log *slog.Logger, res config.Result, source string) {
	if res.Config == nil {
		return
	}

	counts := originCounts(res.Origins)

	attrs := []any{
		"source", source,
		"settings", len(res.Origins),
		"from_file", counts[config.OriginFile],
		"from_environment", counts[config.OriginEnv],
		"at_defaults", counts[config.OriginDefault],
	}

	if changed, err := renderChanged(ctx, res); err == nil {
		attrs = append(attrs, "changed", changed)
	}

	log.Info("configuration resolved", attrs...)

	for canonical, alias := range res.EnvAliases {
		log.Info("setting supplied under an alternative variable name",
			"variable", alias, "canonical", canonical)
	}

	for _, note := range configNotes(res.Config) {
		log.Info(note)
	}
}

// renderChanged produces the same text as `config print --origin --changed`:
// every setting that differs from the built-in default, and which layer supplied
// it. That is the shortest description of a deployment that is still complete,
// and it is exactly what the file would have to contain.
func renderChanged(ctx context.Context, res config.Result) (string, error) {
	defaults, err := config.Defaults(ctx)
	if err != nil {
		return "", fmt.Errorf("config.Defaults: %w", err)
	}

	leaves, err := config.Leaves()
	if err != nil {
		return "", fmt.Errorf("config.Leaves: %w", err)
	}

	selected := make([]config.Leaf, 0, len(leaves))

	for _, leaf := range leaves {
		if !sameValue(leaf, res.Config, defaults) {
			selected = append(selected, leaf)
		}
	}

	opts := printOptions{
		aliases: res.EnvAliases,
		origin:  true,
		changed: true,
	}

	var out strings.Builder

	if err = writeConfig(&out, selected, res.Config, res.Origins, opts); err != nil {
		return "", err
	}

	return out.String(), nil
}

func originCounts(origins config.Origins) map[config.Origin]int {
	counts := map[config.Origin]int{}
	for _, origin := range origins {
		counts[origin]++
	}

	return counts
}

// printOriginSummary counts the settings each layer supplied. A deployment that
// expects its secrets to arrive through the environment can see at a glance
// whether they did.
func printOriginSummary(dst io.Writer, origins config.Origins) {
	counts := originCounts(origins)

	_, _ = fmt.Fprintf(dst, "%d settings: %d from the file, %d from the environment, %d left at defaults.\n",
		len(origins), counts[config.OriginFile], counts[config.OriginEnv], counts[config.OriginDefault])
}

func runConfigPrint(ctx context.Context, opts printOptions) error {
	res, err := resolveForPrint(ctx, opts.cfgPath)
	if err != nil {
		return err
	}

	cfg, origins := res.Config, res.Origins
	opts.aliases = res.EnvAliases

	defaults, err := config.Defaults(ctx)
	if err != nil {
		return fmt.Errorf("config.Defaults: %w", err)
	}

	leaves, err := config.Leaves()
	if err != nil {
		return fmt.Errorf("config.Leaves: %w", err)
	}

	// Selected before anything is written, so that a section whose settings are
	// all filtered out does not get a header with nothing under it.
	selected := make([]config.Leaf, 0, len(leaves))

	for _, leaf := range leaves {
		if opts.changed && sameValue(leaf, cfg, defaults) {
			continue
		}

		selected = append(selected, leaf)
	}

	return writeConfig(os.Stdout, selected, cfg, origins, opts)
}

// resolveForPrint builds the configuration the same way the server does, from a
// file or from the environment alone.
//
// Deliberately the very call the server makes rather than a reimplementation of
// it: a printed configuration that resolved by its own rules would be worse than
// none, because it would be believed.
func resolveForPrint(ctx context.Context, cfgPath string) (config.Result, error) {
	var (
		res config.Result
		err error
	)

	if cfgPath == "" {
		res, err = config.LoadFromEnv(ctx)
	} else {
		res, err = config.Load(ctx, cfgPath)
	}

	// Shown, not swallowed. This command answers "what will actually apply",
	// and a mistyped key is the single most important thing standing between
	// what the operator wrote and what applies.
	reportDiagnostics(os.Stderr, res.Diagnostics)

	// A failure is reported but not fatal here: a configuration that will not
	// start is exactly the one worth being able to look at.
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: this configuration would not start: %v\n", err)
	}

	if res.Config == nil {
		return res, err
	}

	return res, nil
}

// sameValue reports whether a setting still holds what its struct tag default
// gives it. The comparison lives on Leaf so that this command and the test which
// keeps the deployment configs free of restated defaults agree on what "the
// same" means.
func sameValue(leaf config.Leaf, cfg, defaults *config.Config) bool {
	return leaf.SameValue(cfg, defaults)
}

// writeConfig emits the settings as YAML, rebuilding the nesting from the paths.
//
// Written by hand rather than marshalled from the struct because two of the
// three things this command is for happen per setting: replacing a secret with a
// placeholder, and noting which layer supplied it. Marshalling the struct would
// give neither without a shadow type that then has to be kept in step.
func writeConfig(
	dst io.Writer,
	leaves []config.Leaf,
	cfg *config.Config,
	origins config.Origins,
	opts printOptions,
) error {
	var previous []string

	for _, leaf := range leaves {
		parents := leaf.YAMLPath[:len(leaf.YAMLPath)-1]

		// Sections are emitted only where the path diverges from the previous
		// setting's, which is what turns a list of dotted paths back into a
		// nested document. Leaves() walks in declaration order, so the settings
		// of one section arrive together.
		shared := commonPrefix(previous, parents)
		for depth := shared; depth < len(parents); depth++ {
			_, _ = fmt.Fprintf(dst, "%s%s:\n", strings.Repeat("  ", depth), parents[depth])
		}

		previous = parents

		rendered, err := renderValue(leaf, cfg, opts.showSecrets)
		if err != nil {
			return err
		}

		key := leaf.YAMLPath[len(leaf.YAMLPath)-1]
		line := fmt.Sprintf("%s%s: %s", strings.Repeat("  ", len(parents)), key, rendered)

		if opts.origin {
			line = fmt.Sprintf("%-52s # %s", line, describeOrigin(leaf, origins, opts.aliases))
		}

		_, _ = fmt.Fprintln(dst, line)
	}

	return nil
}

// describeOrigin names the layer a setting came from, and for the environment
// the variable itself — which is the part someone chasing an unexpected value
// actually needs.
func describeOrigin(leaf config.Leaf, origins config.Origins, aliases map[string]string) string {
	origin, known := origins[leaf.Name()]
	if !known {
		return "unknown"
	}

	if origin != config.OriginEnv {
		return origin.String()
	}

	// Named as it was actually read. A value that arrived under an alternative
	// spelling and is reported under the canonical one sends the reader to a
	// variable that is not set, which is the failure this command exists to
	// prevent rather than commit.
	if alias, viaAlias := aliases[leaf.EnvKey]; viaAlias {
		return "env " + alias + " (alias for " + leaf.EnvKey + ")"
	}

	return "env " + leaf.EnvKey
}

// renderValue formats one setting as a single-line YAML scalar or flow
// collection.
func renderValue(leaf config.Leaf, cfg *config.Config, showSecrets bool) (string, error) {
	value := leaf.Value(cfg)

	// An empty secret is printed as it is. Replacing it too would report a
	// credential that was never supplied as one that is present and hidden,
	// which is the opposite of what someone checks this output for.
	if leaf.Secret && !showSecrets && !value.IsZero() {
		return redactedValue, nil
	}

	var node yaml.Node
	if err := node.Encode(value.Interface()); err != nil {
		return "", fmt.Errorf("encoding %s: %w", leaf.Name(), err)
	}

	// Lists and maps would otherwise be emitted as indented blocks, and a block
	// cannot carry the origin comment or share a line with its key.
	if node.Kind == yaml.SequenceNode || node.Kind == yaml.MappingNode {
		node.Style = yaml.FlowStyle
	}

	out, err := yaml.Marshal(&node)
	if err != nil {
		return "", fmt.Errorf("marshalling %s: %w", leaf.Name(), err)
	}

	return strings.TrimRight(string(out), "\n"), nil
}

// commonPrefix returns how many leading elements two paths share.
func commonPrefix(left, right []string) int {
	shared := 0
	for shared < len(left) && shared < len(right) && left[shared] == right[shared] {
		shared++
	}

	return shared
}
