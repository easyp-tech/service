package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// TestCheckFilesCatchesAMistypedLicencePath covers the most expensive quiet
// failure this service had.
//
// A LICENSE_FILE pointing at nothing passed validation, and the service then
// started in community mode and served every request correctly. A paid
// deployment silently became a free one, and the only evidence was one log line.
func TestCheckFilesCatchesAMistypedLicencePath(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}
	cfg.License.File = filepath.Join(t.TempDir(), "does-not-exist.token")

	diags := cfg.CheckFiles()

	require.Len(t, diags, 1)
	require.Equal(t, "license.file", diags[0].Path)
	require.Equal(t, config.SeverityError, diags[0].Severity)
}

// TestCheckFilesAcceptsWhatItCanRead keeps the check from being noise: a path
// that exists must pass, and an unset one must not be invented.
func TestCheckFilesAcceptsWhatItCanRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	present := filepath.Join(dir, "licence.token")
	require.NoError(t, os.WriteFile(present, []byte("token"), 0o600))

	cfg := config.Config{}
	cfg.License.File = present

	require.Empty(t, cfg.CheckFiles())

	// Unset paths are not checked at all: no TLS is a valid configuration.
	empty := config.Config{}
	require.Empty(t, empty.CheckFiles())
}

// TestCheckFilesNamesEveryMissingPath makes sure the operator fixes one thing
// and sees the next, rather than one at a time across three restarts.
func TestCheckFilesNamesEveryMissingPath(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nope")

	cfg := config.Config{}
	cfg.Server.TLS.CertFile = missing
	cfg.Server.TLS.KeyFile = missing
	cfg.Server.TLS.ClientCAFile = missing
	cfg.License.File = missing

	names := make([]string, 0, 4)
	for _, diag := range cfg.CheckFiles() {
		names = append(names, diag.Path)
	}

	require.ElementsMatch(t, []string{
		"server.tls.cert_file",
		"server.tls.key_file",
		"server.tls.client_ca_file",
		"license.file",
	}, names)
}
