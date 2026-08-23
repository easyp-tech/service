package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
