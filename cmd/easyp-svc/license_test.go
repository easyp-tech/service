package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aidanwoods.dev/go-paseto"
	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/core"
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

func TestResolveLicensePublicKeys(t *testing.T) {
	const (
		keyA = "aa" + "00000000000000000000000000000000000000000000000000000000000000"
		keyB = "bb" + "00000000000000000000000000000000000000000000000000000000000000"
	)

	t.Run("nothing configured means no keys", func(t *testing.T) {
		t.Setenv(licensePublicKeysEnv, "")

		require.Nil(t, resolveLicensePublicKeys(config.LicenseConfig{})) //nolint:exhaustruct
	})

	t.Run("config wins over environment", func(t *testing.T) {
		t.Setenv(licensePublicKeysEnv, "from-env:"+keyB)

		keys := resolveLicensePublicKeys(config.LicenseConfig{ //nolint:exhaustruct
			PublicKeys: map[string]string{"2026-08": keyA},
		})
		require.Equal(t, map[string]string{"2026-08": keyA}, keys)
	})

	t.Run("environment is the fallback the --cfg path relies on", func(t *testing.T) {
		t.Setenv(licensePublicKeysEnv, " 2026-08:"+keyA+" , 2026-09:"+keyB+" ")

		keys := resolveLicensePublicKeys(config.LicenseConfig{}) //nolint:exhaustruct
		require.Equal(t, map[string]string{"2026-08": keyA, "2026-09": keyB}, keys)
	})

	t.Run("entries with no separator are skipped", func(t *testing.T) {
		t.Setenv(licensePublicKeysEnv, "junk,2026-08:"+keyA)

		keys := resolveLicensePublicKeys(config.LicenseConfig{}) //nolint:exhaustruct
		require.Equal(t, map[string]string{"2026-08": keyA}, keys)
	})

	t.Run("a value with nothing usable in it means no keys", func(t *testing.T) {
		t.Setenv(licensePublicKeysEnv, ",,:,")

		require.Nil(t, resolveLicensePublicKeys(config.LicenseConfig{})) //nolint:exhaustruct
	})
}

// renderedPublicKeys is what the Helm chart writes into LICENSE_PUBLIC_KEYS, and
// what it decodes to. Both paths that read the variable are pinned against it
// below: a chart value only one of them understands is a setting that works in
// one deployment and silently does nothing in the other.
func renderedPublicKeys() (string, map[string]string) {
	keyA, keyB := strings.Repeat("a", 64), strings.Repeat("b", 64)

	return "2026-08:" + keyA + ",2026-09:" + keyB, map[string]string{"2026-08": keyA, "2026-09": keyB}
}

// TestLicensePublicKeysEnvconfigDecoding covers the path the chart actually
// takes: the container gets environment variables and no --cfg.
func TestLicensePublicKeysEnvconfigDecoding(t *testing.T) {
	t.Parallel()

	rendered, want := renderedPublicKeys()

	var cfg config.Config //nolint:exhaustruct

	err := envconfig.ProcessWith(t.Context(), &envconfig.Config{ //nolint:exhaustruct
		Target:   &cfg,
		Lookuper: envconfig.MapLookuper(map[string]string{licensePublicKeysEnv: rendered}),
	})
	require.NoError(t, err)
	require.Equal(t, want, cfg.License.PublicKeys)
}

// TestLicensePublicKeysCfgFallbackDecoding covers the path docker-compose takes,
// where --cfg means envconfig never runs and the variable is read by hand.
func TestLicensePublicKeysCfgFallbackDecoding(t *testing.T) {
	rendered, want := renderedPublicKeys()

	t.Setenv(licensePublicKeysEnv, rendered)

	require.Equal(t, want, resolveLicensePublicKeys(config.LicenseConfig{})) //nolint:exhaustruct
}

// mintLicence issues a token of the shape the service expects. The issuer and
// audience are spelled out rather than borrowed from the license package: this
// test is the other side of that contract, and it should fail if the contract
// changes under it.
func mintLicence(t *testing.T, key paseto.V4AsymmetricSecretKey, kid string) string {
	t.Helper()

	token := paseto.NewToken()
	token.SetIssuer("easyp.tech")
	token.SetAudience("easyp-service")
	token.SetIssuedAt(time.Now())
	token.SetNotBefore(time.Now().Add(-time.Hour))
	token.SetExpiration(time.Now().Add(30 * 24 * time.Hour))
	token.SetString("tier", "enterprise")
	token.SetString("customer_name", "acme")

	footer, err := json.Marshal(map[string]string{"kid": kid})
	require.NoError(t, err)
	token.SetFooter(footer)

	return token.V4Sign(key, nil)
}

// TestBuildLicenseClient covers the composition root: what the service actually
// runs with, rather than what the licence package is capable of.
//
// The first case is the one that matters. A unit test on the verifier stayed
// green for as long as this binary was wired to a placeholder that took any
// non-empty token at face value; only a test here can tell the difference.
func TestBuildLicenseClient(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	signing := paseto.NewV4AsymmetricSecretKey()
	publicKey := signing.Public().ExportHex()

	other := paseto.NewV4AsymmetricSecretKey()

	tierFor := func(t *testing.T, cfg config.LicenseConfig) string {
		t.Helper()

		client, err := buildLicenseClient(cfg, log)
		require.NoError(t, err)

		claims, err := client.ValidateLicense(t.Context())
		require.NoError(t, err)

		return claims.Tier
	}

	t.Run("an arbitrary string is not a licence", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		tier := tierFor(t, config.LicenseConfig{ //nolint:exhaustruct
			Key:        "any-old-string",
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("a token signed by someone else is not a licence", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		tier := tierFor(t, config.LicenseConfig{ //nolint:exhaustruct
			Key:        mintLicence(t, other, "2026-08"),
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("a token signed by the configured key is a licence", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		tier := tierFor(t, config.LicenseConfig{ //nolint:exhaustruct
			Key:        mintLicence(t, signing, "2026-08"),
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierEnterprise, tier)
	})

	t.Run("a valid token with no key to check it against is community mode", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		tier := tierFor(t, config.LicenseConfig{ //nolint:exhaustruct
			Key: mintLicence(t, signing, "2026-08"),
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("the single-key setting still works", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		tier := tierFor(t, config.LicenseConfig{ //nolint:exhaustruct
			Key:       mintLicence(t, signing, "2026-08"),
			PublicKey: publicKey,
		})
		require.Equal(t, core.LicenseTierEnterprise, tier)
	})

	t.Run("keys arrive through the environment on the --cfg path", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, mintLicence(t, signing, "2026-08"))
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "2026-08:"+publicKey)

		require.Equal(t, core.LicenseTierEnterprise, tierFor(t, config.LicenseConfig{})) //nolint:exhaustruct
	})

	t.Run("a mistyped key stops startup rather than quietly downgrading", func(t *testing.T) {
		t.Setenv(licenseTokenEnv, "")
		t.Setenv(licensePublicKeyEnv, "")
		t.Setenv(licensePublicKeysEnv, "")

		_, err := buildLicenseClient(config.LicenseConfig{ //nolint:exhaustruct
			Key:        mintLicence(t, signing, "2026-08"),
			PublicKeys: map[string]string{"2026-08": "not-a-key"},
		}, log)
		require.Error(t, err)
		require.Contains(t, err.Error(), "2026-08")
	})
}

func TestResolveWriteToken(t *testing.T) {
	t.Run("flag wins over environment", func(t *testing.T) {
		t.Setenv(writeTokenEnv, "from-env")

		require.Equal(t, "from-flag", resolveWriteToken("from-flag"))
	})

	t.Run("environment is the fallback", func(t *testing.T) {
		t.Setenv(writeTokenEnv, "  from-env\n")

		require.Equal(t, "from-env", resolveWriteToken(""))
	})

	t.Run("nothing configured means an unauthenticated call", func(t *testing.T) {
		t.Setenv(writeTokenEnv, "")

		require.Empty(t, resolveWriteToken(""))
	})
}
