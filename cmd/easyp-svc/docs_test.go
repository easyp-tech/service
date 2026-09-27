package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
	"github.com/easyp-tech/service/internal/config"
)

// envAssignment matches a NAME=... line inside a shell block in the README.
var envAssignment = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)=`)

// TestReadmeNamesOnlyRealVariables is the part of the documentation fix that
// lasts.
//
// The rest of it was a one-off correction of drift that had already happened
// twice: the README listed DB_MIGRATE_DIR, which never existed, and the public
// documentation site taught EASYP_SERVICE_HOST and EASYP_SERVICE_DB_PASSWORD,
// which this service has never read — so someone following it configured a
// product that does not exist and got no error from anything.
//
// Nothing but a test stops that recurring, because a wrong variable name looks
// exactly like a right one until someone deploys it.
func TestReadmeNamesOnlyRealVariables(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	known := make(map[string]bool, len(leaves))
	for _, leaf := range leaves {
		known[leaf.EnvKey] = true
	}

	// Real names that are not settings of this Config, so the check does not
	// reject the README for documenting them: the client's credential, the
	// compose-level variables, and the ones `task push-archives` reads.
	for _, name := range []string{
		"EASYP_TOKEN",
		"EASYP_SERVICE_VERSION",
		"LOG_LEVEL",
		"S3_ENDPOINT",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
	} {
		known[name] = true
	}

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	var unknown []string

	for _, match := range envAssignment.FindAllStringSubmatch(string(readme), -1) {
		name := match[1]
		if !known[name] && strings.Contains(name, "_") {
			unknown = append(unknown, name)
		}
	}

	require.Empty(t, unknown,
		"the README names variables the service does not read; every one of these configures nothing")
}

// TestReadmeMentionsTheDiagnosticCommands guards the finding every audit made
// independently: `config print` and `config validate` answer the two questions a
// config file raises, and the README — the one entry point for someone who does
// not read Go — did not mention either of them once. They were documented only
// in the header comment of a deploy file, which you have to already have opened.
func TestReadmeMentionsTheDiagnosticCommands(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.Contains(t, text, "config validate")
	require.Contains(t, text, "config print")
	require.Contains(t, text, "--origin")
}

// TestReadmeHasNoGhostCommands pins two instructions that pointed at nothing:
// a register-plugins.sh that is not in the repository, and a binary path from
// before the commands were merged into easyp-svc. Both sat next to working
// instructions in the same file.
func TestReadmeHasNoGhostCommands(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.NotContains(t, text, "register-plugins.sh", "the file does not exist; the task does")
	require.NotContains(t, text, "./cmd/main.go", "the binary is ./cmd/easyp-svc")
	require.NotContains(t, text, "bin/server", "the binary is easyp-svc")
}

// TestReadmeUsesTheRenamedService pins the rename that v0.14.0 made and the
// README missed for four months: the service is easyp.generator.v1.GeneratorAPI,
// and every client built against the old name was refused by the stand until
// easyp v0.17.0. A README still teaching ServiceAPI teaches a name the server
// does not answer to.
func TestReadmeUsesTheRenamedService(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.Contains(t, text, generator.GeneratorAPI_ServiceDesc.ServiceName)
	require.NotContains(t, text, "service ServiceAPI", "renamed to GeneratorAPI in v0.14.0")
	require.NotContains(t, text, "ServiceAPI/", "no RPC path under the old name")
	require.NotContains(t, text, "api.generator.v1", "the proto package is easyp.generator.v1")
}

// TestReadmeNamesRealHealthPaths keeps /health out of the README. The health
// listener serves /live and /, and /health only answers because / is a
// catch-all — so the documented path works by accident and the real ones are
// never mentioned.
func TestReadmeNamesRealHealthPaths(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.Contains(t, text, healthLivePath)
	require.NotContains(t, text, "/health", "the listener serves /live and /, not /health")
}

// TestReadmeNamesOnlyRealMetrics does for metric names what
// TestReadmeNamesOnlyRealVariables does for environment variables: every
// easyp_* name in the README must be one the service registers. The README
// carried easyp_business_plugins_total, which nothing has ever exported, and a
// dashboard built from it shows an empty panel rather than an error.
func TestReadmeNamesOnlyRealMetrics(t *testing.T) {
	t.Parallel()

	known := registeredMetricNames(t)

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	var unknown []string

	for _, name := range metricMention.FindAllString(string(readme), -1) {
		if known[name] || notMetrics[name] || strings.HasPrefix(name, "easyp_api_grpc_") {
			continue
		}

		unknown = append(unknown, name)
	}

	require.Empty(t, unknown, "the README names metrics the service does not export")
}

// notMetrics are easyp_-prefixed identifiers the README legitimately uses
// that are not metrics: the database role, database names and the bucket.
var notMetrics = map[string]bool{
	"easyp_svc":           true,
	"easyp_pass":          true,
	"easyp_db":            true,
	"easyp_community_db":  true,
	"easyp_enterprise_db": true,
	"easyp_plugins":       true,
	"easyp_network":       true,
}

// metricMention matches an easyp_-prefixed metric name in prose or code.
var metricMention = regexp.MustCompile(`\beasyp_[a-z_]+[a-z]\b`)

// metricName matches the Name field of a prometheus Opts literal.
var metricName = regexp.MustCompile(`Name:\s+"([a-z_]+)"`)

// registeredMetricNames collects every metric name the service's own code
// declares, prefixed with the easyp namespace. The gRPC middleware's metrics
// are not declared here and are allowed by prefix in the caller.
func registeredMetricNames(t *testing.T) map[string]bool {
	t.Helper()

	names := map[string]bool{}

	err := filepath.WalkDir("../../internal", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for _, m := range metricName.FindAllStringSubmatch(string(src), -1) {
			names["easyp_"+m[1]] = true
		}

		return nil
	})
	require.NoError(t, err)

	return names
}

// TestReadmeListsEveryPluginGroup: the registry has ten groups and the README
// listed four, which is how a reader concludes the catalogue is four plugins.
func TestReadmeListsEveryPluginGroup(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("../../registry")
	require.NoError(t, err)

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		require.Contains(t, text, "`"+e.Name()+"`", "plugin group %s is in registry/ but not in the README", e.Name())
	}
}

// TestReadmeNamesOnlyRealMCPTools pins the tool list to what the MCP endpoint
// registers. easyp_config_describe was removed in v0.14.0 and the README kept
// offering it.
func TestReadmeNamesOnlyRealMCPTools(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.Contains(t, text, "plugins_list")
	require.NotContains(t, text, "easyp_config_describe", "removed in v0.14.0")
}

// TestReadmeDescribesTheCurrentRegistryLayout pins the shape of registry/:
// one directory per plugin with a plugin.yaml listing its versions and a
// Dockerfile that takes ARG VERSION. The README described a version directory
// per plugin and recommended upx, which no Dockerfile in the registry uses.
func TestReadmeDescribesTheCurrentRegistryLayout(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)

	text := string(readme)
	require.Contains(t, text, "plugin.yaml")
	require.Contains(t, text, "ARG VERSION")
	require.NotContains(t, text, "{plugin-name}/{version}", "versions live in plugin.yaml, not in a directory")
	require.NotRegexp(t, `registry/[a-z-]+/[a-z-]+/v\d`, text, "registry/ has no version directories")
	require.NotContains(t, text, "upx", "no registry Dockerfile uses it")
}
