package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

func TestResolveLicenseToken(t *testing.T) {
	t.Run("nothing configured means community mode", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")

		token, err := resolveLicenseToken(config.LicenseConfig{}) //nolint:exhaustruct
		require.NoError(t, err)
		require.Empty(t, token)
	})

	t.Run("inline key wins over file and environment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "licence")
		require.NoError(t, os.WriteFile(path, []byte("from-file"), 0o600))
		t.Setenv(licenseTokenEnv, "from-env")

		token, err := resolveLicenseToken(config.LicenseConfig{Key: "inline", File: path}) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "inline", token)
	})

	t.Run("file wins over environment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "licence")
		require.NoError(t, os.WriteFile(path, []byte("  from-file\n"), 0o600))
		t.Setenv(licenseTokenEnv, "from-env")

		token, err := resolveLicenseToken(config.LicenseConfig{File: path}) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "from-file", token, "surrounding whitespace must be stripped")
	})

	t.Run("environment is the fallback the --cfg path relies on", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, " from-env\n")

		token, err := resolveLicenseToken(config.LicenseConfig{}) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "from-env", token)
	})

	t.Run("an unreadable file is an error, not a silent downgrade", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "from-env")

		_, err := resolveLicenseToken(config.LicenseConfig{ //nolint:exhaustruct
			File: filepath.Join(t.TempDir(), "absent"),
		})
		require.Error(t, err)
	})
}

func TestResolveLicensePublicKey(t *testing.T) {
	t.Run("nothing configured means no verification key", func(t *testing.T) {
		t.Setenv(licensePublicKeyEnv, "")

		require.Empty(t, resolveLicensePublicKey(config.LicenseConfig{})) //nolint:exhaustruct
	})

	t.Run("config wins over environment", func(t *testing.T) {
		t.Setenv(licensePublicKeyEnv, "from-env")

		key := resolveLicensePublicKey(config.LicenseConfig{PublicKey: "from-config"}) //nolint:exhaustruct
		require.Equal(t, "from-config", key)
	})

	t.Run("environment is the fallback the --cfg path relies on", func(t *testing.T) {
		t.Setenv(licensePublicKeyEnv, " from-env\n")

		require.Equal(t, "from-env", resolveLicensePublicKey(config.LicenseConfig{})) //nolint:exhaustruct
	})
}

func TestResolveLicense(t *testing.T) {
	t.Run("collects both halves", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "env-token")
		t.Setenv(licensePublicKeyEnv, "env-key")

		creds, err := resolveLicense(config.LicenseConfig{}) //nolint:exhaustruct
		require.NoError(t, err)
		require.Equal(t, "env-token", creds.token)
		require.Equal(t, "env-key", creds.publicKey)
	})

	t.Run("a bad token file fails the whole resolution", func(t *testing.T) {
		t.Setenv(licensePublicKeyEnv, "env-key")

		_, err := resolveLicense(config.LicenseConfig{ //nolint:exhaustruct
			File: filepath.Join(t.TempDir(), "absent"),
		})
		require.Error(t, err)
	})
}
