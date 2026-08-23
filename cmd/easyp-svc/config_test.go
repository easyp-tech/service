package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// writeConfigFixture writes a config that loads cleanly, so each test below can
// look at one aspect of the printed output. registryExtra extends the registry
// section in place and extra appends whole sections the base does not name —
// YAML has no merge for a repeated top-level key, it is an error.
func writeConfigFixture(t *testing.T, registryExtra, extra string) string {
	t.Helper()

	doc := `
server:
  host: "0.0.0.0"
  port: {grpc: "8080", metric: "8081", health: "8082", mcp: "8083"}
  force_shutdown_after: 150s
  max_send_msg_size: 67108864
  trusted_proxies: ["172.28.0.0/16", "10.0.0.0/8"]
db: {postgres: "postgres://user:hunter2@host:5432/d?sslmode=disable"}
registry: {plugins_dir: /plugins, max_output_size: 67108864` + registryExtra + `}
worker_pool:
  workers: 4
  queue_size: 16
  max_concurrent_generations: 16
  generation_timeout: 120s
  shutdown_timeout: 30s
rate_limit: {requests_per_second: 10.0, burst: 20, cleanup_interval: 10m}
audit: {buffer_size: 1000, batch_size: 100, flush_interval: 1s}
` + extra

	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))

	return path
}

// printed renders the configuration the way the command does, into a buffer.
func printed(t *testing.T, path string, opts printOptions) string {
	t.Helper()

	opts.cfgPath = path

	res, err := resolveForPrint(t.Context(), path)
	require.NoError(t, err)

	cfg, origins := res.Config, res.Origins
	opts.aliases = res.EnvAliases

	defaults, err := config.Defaults(t.Context())
	require.NoError(t, err)

	leaves, err := config.Leaves()
	require.NoError(t, err)

	selected := make([]config.Leaf, 0, len(leaves))

	for _, leaf := range leaves {
		if opts.changed && sameValue(leaf, cfg, defaults) {
			continue
		}

		selected = append(selected, leaf)
	}

	var out bytes.Buffer
	require.NoError(t, writeConfig(&out, selected, cfg, origins, opts))

	return out.String()
}

// TestPrintRedactsSecrets is the check that matters most: this output is what
// someone pastes into an issue or a chat when asking why a deployment is
// misbehaving, and the database password travels in the DSN.
func TestPrintRedactsSecrets(t *testing.T) {
	t.Parallel()

	path := writeConfigFixture(t, "", "")

	out := printed(t, path, printOptions{})

	require.NotContains(t, out, "hunter2", "the database password must not be printed")
	require.Contains(t, out, `postgres: "***"`)

	// And the escape hatch works, because there are times you need the value.
	revealed := printed(t, path, printOptions{showSecrets: true})
	require.Contains(t, revealed, "hunter2")
}

// TestPrintKeepsAnAbsentSecretVisible covers the trap in redaction. Replacing an
// empty credential with a placeholder would report a secret that was never
// supplied as one that is present and merely hidden — and "did my key reach the
// container" is the question this command is most often asked.
func TestPrintKeepsAnAbsentSecretVisible(t *testing.T) {
	t.Parallel()

	out := printed(t, writeConfigFixture(t, "", ""), printOptions{})

	require.Contains(t, out, `license:`)
	require.Contains(t, out, `key: ""`, "an unset licence must read as unset, not as hidden")
}

// TestPrintChangedHidesWhatTheDefaultsAlreadySay is the property Этап 4 leans
// on: a config file needs to state what differs and nothing else.
func TestPrintChangedHidesWhatTheDefaultsAlreadySay(t *testing.T) {
	t.Parallel()

	path := writeConfigFixture(t, ", cache_max_bytes: 5", "")

	out := printed(t, path, printOptions{changed: true})

	require.Contains(t, out, "cache_max_bytes: 5", "a value that differs from the default is the point")
	require.NotContains(t, out, "buffer_size", "audit.buffer_size restates its default and says nothing")
	require.NotContains(t, out, "host:", "server.host restates its default too")
}

// TestPrintChangedTreatsEmptyCollectionsAsUnset pins the distinction that would
// otherwise make every shipped config look like it carried information it does
// not: `write_tokens: []` and an absent key are the same setting.
func TestPrintChangedTreatsEmptyCollectionsAsUnset(t *testing.T) {
	t.Parallel()

	path := writeConfigFixture(t, "", "auth:\n  write_tokens: []\nlicense:\n  public_keys: {}\n")

	out := printed(t, path, printOptions{changed: true})

	require.NotContains(t, out, "write_tokens")
	require.NotContains(t, out, "public_keys")
}

// TestPrintReportsOrigins covers the annotation. The value alone does not answer
// the question that gets asked — which of the three layers put it there — and
// for the environment the variable's name is the actionable part.
func TestPrintReportsOrigins(t *testing.T) {
	// Not parallel: it sets a variable, and the resolution it is checking reads
	// the process environment.
	t.Setenv("REGISTRY_S3_BUCKET", "from-the-environment")

	path := writeConfigFixture(t, "", "")

	out := printed(t, path, printOptions{origin: true})

	requireLineMatching(t, out, "bucket:", "env REGISTRY_S3_BUCKET")
	requireLineMatching(t, out, "workers:", "# file")
	requireLineMatching(t, out, "enqueue_timeout:", "# default")
}

// TestPrintProducesParseableYAML guards the hand-rolled emitter. Rebuilding the
// nesting from dotted paths is the one part of this that can be quietly wrong,
// and wrong output is worse than none: it would be pasted into a config file.
func TestPrintProducesParseableYAML(t *testing.T) {
	t.Parallel()

	path := writeConfigFixture(t, "", "")

	// With secrets, because that is what a round trip means: the redacted form
	// is not a configuration the service would start on, and since v0.13.0 it
	// says so — a DSN of "***" fails the connection-string check rather than
	// passing as an ordinary one, which is what it used to do.
	out := printed(t, path, printOptions{showSecrets: true})

	// Round-tripped through the loader itself rather than a bare YAML parse, so
	// that the keys have to be the real ones and in the right sections.
	roundTrip := filepath.Join(t.TempDir(), "printed.yml")
	require.NoError(t, os.WriteFile(roundTrip, []byte(out), 0o600))

	res, err := config.Load(t.Context(), roundTrip)
	require.NoError(t, err, "printed output must be a config file the service accepts")
	require.Empty(t, res.Diagnostics, "every key printed must be one the service recognises")

	reloaded := res.Config
	require.Equal(t, []string{"172.28.0.0/16", "10.0.0.0/8"}, reloaded.Server.TrustedProxies,
		"a list must survive being printed and read back")
	require.Equal(t, "8080", reloaded.Server.Port.GRPC)
	require.Equal(t, 16, reloaded.WorkerPool.MaxConcurrentGenerations)
}

func requireLineMatching(t *testing.T, out, key, want string) {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, key) {
			require.Contains(t, line, want)

			return
		}
	}

	t.Fatalf("no line containing %q in:\n%s", key, out)
}

// TestRedactedOutputIsNotAConfiguration pins a defect the audits found: the
// output of `config print --changed` without --show-secrets was accepted as a
// configuration, because a DSN of "***" was only ever checked for being
// non-empty. Someone saving that output as their config file got a service that
// validated and then could not reach a database.
func TestRedactedOutputIsNotAConfiguration(t *testing.T) {
	t.Parallel()

	path := writeConfigFixture(t, "", "")
	out := printed(t, path, printOptions{})

	require.Contains(t, out, redactedValue, "the fixture's DSN is a secret and must be hidden")

	redacted := filepath.Join(t.TempDir(), "redacted.yml")
	require.NoError(t, os.WriteFile(redacted, []byte(out), 0o600))

	_, err := config.Load(t.Context(), redacted)
	require.ErrorContains(t, err, "db.postgres is not a usable connection string")
}
