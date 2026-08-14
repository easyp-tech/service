package main

import (
	"context"
	"fmt"
	"io"
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
	if cfgPath == "" {
		cfg := config.Config{} //nolint:exhaustruct // filled from the environment

		if err := config.ApplyEnv(ctx, &cfg); err != nil {
			return fmt.Errorf("config.ApplyEnv: %w", err)
		}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("config validation: %w", err)
		}

		origins, err := config.EnvironmentOrigins()
		if err != nil {
			return fmt.Errorf("config.EnvironmentOrigins: %w", err)
		}

		_, _ = fmt.Fprintln(os.Stdout, "Configuration from the environment is valid.")
		printOriginSummary(os.Stdout, origins)

		return nil
	}

	cfg, warnings, origins, err := config.LoadAndValidateWithOrigins(ctx, cfgPath)

	// Printed before the error is returned: an unknown field is the likeliest
	// explanation for a validation failure right after someone edited the file.
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(os.Stderr, "warning: unrecognised field, ignored: %s\n", warning)
	}

	if err != nil {
		return fmt.Errorf("config.LoadAndValidateWithOrigins: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "%s is valid.\n", cfgPath)
	printOriginSummary(os.Stdout, origins)

	// Not silent about it: a config whose S3 section is filled but whose
	// credentials are absent is valid and still cannot read a plugin, and the
	// person running this is the one who can still do something about it.
	if cfg.Registry.S3.Enabled() && cfg.Registry.S3.AccessKeyID == "" {
		_, _ = fmt.Fprintln(os.Stdout,
			"note: object storage is configured with no credentials; "+
				"the default AWS credential chain will be used")
	}

	return nil
}

// printOriginSummary counts the settings each layer supplied. A deployment that
// expects its secrets to arrive through the environment can see at a glance
// whether they did.
func printOriginSummary(dst io.Writer, origins config.Origins) {
	counts := map[config.Origin]int{}
	for _, origin := range origins {
		counts[origin]++
	}

	_, _ = fmt.Fprintf(dst, "%d settings: %d from the file, %d from the environment, %d left at defaults.\n",
		len(origins), counts[config.OriginFile], counts[config.OriginEnv], counts[config.OriginDefault])
}

func runConfigPrint(ctx context.Context, opts printOptions) error {
	cfg, origins, err := resolveForPrint(ctx, opts.cfgPath)
	if err != nil {
		return err
	}

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
func resolveForPrint(ctx context.Context, cfgPath string) (*config.Config, config.Origins, error) {
	if cfgPath == "" {
		cfg := config.Config{} //nolint:exhaustruct // filled from the environment

		if err := config.ApplyEnv(ctx, &cfg); err != nil {
			return nil, nil, fmt.Errorf("config.ApplyEnv: %w", err)
		}

		origins, err := config.EnvironmentOrigins()
		if err != nil {
			return nil, nil, fmt.Errorf("config.EnvironmentOrigins: %w", err)
		}

		return &cfg, origins, nil
	}

	// Validation failures are reported but not fatal: a configuration that will
	// not start is exactly the one worth being able to look at.
	cfg, _, origins, err := config.LoadAndValidateWithOrigins(ctx, cfgPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: this configuration would not start: %v\n", err)
	}

	if cfg == nil {
		return nil, nil, fmt.Errorf("config.LoadAndValidateWithOrigins: %w", err)
	}

	return cfg, origins, nil
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
			line = fmt.Sprintf("%-52s # %s", line, describeOrigin(leaf, origins))
		}

		_, _ = fmt.Fprintln(dst, line)
	}

	return nil
}

// describeOrigin names the layer a setting came from, and for the environment
// the variable itself — which is the part someone chasing an unexpected value
// actually needs.
func describeOrigin(leaf config.Leaf, origins config.Origins) string {
	origin, known := origins[leaf.Name()]
	if !known {
		return "unknown"
	}

	if origin == config.OriginEnv {
		return "env " + leaf.EnvKey
	}

	return origin.String()
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
