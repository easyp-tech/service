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
	"github.com/easyp-tech/service/internal/license"
)

// licensePublicKeysEnv is spelled out rather than imported: this test is the
// other side of the contract with the Helm chart and docker-compose, and it
// should fail if the variable is ever renamed rather than follow it.
const licensePublicKeysEnv = "LICENSE_PUBLIC_KEYS"

func TestResolveLicenseToken(t *testing.T) {
	t.Parallel()

	t.Run("nothing configured means community mode", func(t *testing.T) {
		t.Parallel()

		token, err := resolveLicenseToken(config.LicenseConfig{})
		require.NoError(t, err)
		require.Empty(t, token)
	})

	t.Run("inline key wins over file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "licence")
		require.NoError(t, os.WriteFile(path, []byte("from-file"), 0o600))

		token, err := resolveLicenseToken(config.LicenseConfig{Key: "inline", File: path})
		require.NoError(t, err)
		require.Equal(t, "inline", token)
	})

	t.Run("the file is read and trimmed", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "licence")
		require.NoError(t, os.WriteFile(path, []byte("  from-file\n"), 0o600))

		token, err := resolveLicenseToken(config.LicenseConfig{File: path})
		require.NoError(t, err)
		require.Equal(t, "from-file", token, "surrounding whitespace must be stripped")
	})

	t.Run("an unreadable file is an error, not a silent downgrade", func(t *testing.T) {
		t.Parallel()

		_, err := resolveLicenseToken(config.LicenseConfig{
			File: filepath.Join(t.TempDir(), "absent"),
		})
		require.Error(t, err)
	})
}

func TestTrimmedPublicKeys(t *testing.T) {
	t.Parallel()

	const (
		keyA = "aa" + "00000000000000000000000000000000000000000000000000000000000000"
		keyB = "bb" + "00000000000000000000000000000000000000000000000000000000000000"
	)

	t.Run("nothing configured means no keys", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, trimmedPublicKeys(config.LicenseConfig{}))
	})

	t.Run("whitespace around ids and keys is stripped", func(t *testing.T) {
		t.Parallel()

		keys := trimmedPublicKeys(config.LicenseConfig{
			// The whitespace is the point: a key id wrapped across lines in YAML,
			// or a list written with a space after the comma, must still match.
			//nolint:gocritic // suspicious whitespace in the key is what is under test
			PublicKeys: map[string]string{" 2026-08 ": " " + keyA + "\n", "2026-09": keyB},
		})
		require.Equal(t, map[string]string{"2026-08": keyA, "2026-09": keyB}, keys)
	})

	t.Run("entries with an empty half are skipped", func(t *testing.T) {
		t.Parallel()

		keys := trimmedPublicKeys(config.LicenseConfig{
			PublicKeys: map[string]string{"": keyA, "2026-09": "  "},
		})
		require.Nil(t, keys, "nothing usable must read as no keys at all")
	})
}

// TestLicensePublicKeysDecoding pins the encoding the Helm chart writes into
// LICENSE_PUBLIC_KEYS against what envconfig makes of it. Both startup paths go
// through envconfig now, so one case covers what used to need two: a chart value
// that decoded on one path and silently did nothing on the other is no longer
// possible to express.
func TestLicensePublicKeysDecoding(t *testing.T) {
	t.Parallel()

	keyA, keyB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	rendered := "2026-08:" + keyA + ",2026-09:" + keyB
	want := map[string]string{"2026-08": keyA, "2026-09": keyB}

	var cfg config.Config

	err := envconfig.ProcessWith(t.Context(), &envconfig.Config{
		Target:   &cfg,
		Lookuper: envconfig.MapLookuper(map[string]string{licensePublicKeysEnv: rendered}),
	})
	require.NoError(t, err)
	require.Equal(t, want, cfg.License.PublicKeys)
}

// TestLicensePublicKeysOverrideEmptyYAMLMap is the regression guard for the trap
// that makes the whole layering work: `public_keys: {}` in a shipped config
// decodes to a non-nil empty map, for which reflect.IsZero is false. Without
// DefaultOverwrite envconfig would skip the field entirely and never look the
// variable up — the enterprise container would run as community, and only
// deploy/scripts/check-tiers.sh would notice.
func TestLicensePublicKeysOverrideEmptyYAMLMap(t *testing.T) {
	t.Parallel()

	key := strings.Repeat("c", 64)

	cfg := config.Config{}
	cfg.License.PublicKeys = map[string]string{}
	cfg.Server.TrustedProxies = []string{}

	err := envconfig.ProcessWith(t.Context(), &envconfig.Config{
		Target:           &cfg,
		DefaultOverwrite: true,
		Lookuper:         envconfig.MapLookuper(map[string]string{licensePublicKeysEnv: "2026-08:" + key}),
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"2026-08": key}, cfg.License.PublicKeys,
		"an empty map from YAML must not block the environment")
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
	t.Parallel()

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
		t.Parallel()

		tier := tierFor(t, config.LicenseConfig{
			Key:        "any-old-string",
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("a token signed by someone else is not a licence", func(t *testing.T) {
		t.Parallel()

		tier := tierFor(t, config.LicenseConfig{
			Key:        mintLicence(t, other, "2026-08"),
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("a token signed by the configured key is a licence", func(t *testing.T) {
		t.Parallel()

		tier := tierFor(t, config.LicenseConfig{
			Key:        mintLicence(t, signing, "2026-08"),
			PublicKeys: map[string]string{"2026-08": publicKey},
		})
		require.Equal(t, core.LicenseTierEnterprise, tier)
	})

	t.Run("a valid token with no key to check it against is community mode", func(t *testing.T) {
		t.Parallel()

		tier := tierFor(t, config.LicenseConfig{
			Key: mintLicence(t, signing, "2026-08"),
		})
		require.Equal(t, core.LicenseTierCommunity, tier)
	})

	t.Run("the reserved key id covers any key id", func(t *testing.T) {
		t.Parallel()

		// What license.public_key used to express, now an entry in the one map
		// that describes the trust anchor.
		tier := tierFor(t, config.LicenseConfig{
			Key:        mintLicence(t, signing, "2026-08"),
			PublicKeys: map[string]string{license.AnyKeyID: publicKey},
		})
		require.Equal(t, core.LicenseTierEnterprise, tier)
	})

	t.Run("a mistyped key stops startup rather than quietly downgrading", func(t *testing.T) {
		t.Parallel()

		_, err := buildLicenseClient(config.LicenseConfig{
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
