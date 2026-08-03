package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/license"
)

// These variables are consulted directly because the --cfg path decodes YAML
// and skips envconfig entirely, so licence settings handed to the container
// through the environment would otherwise be dropped on the floor.
const (
	licenseTokenEnv      = "LICENSE_KEY"
	licensePublicKeyEnv  = "LICENSE_PUBLIC_KEY"
	licensePublicKeysEnv = "LICENSE_PUBLIC_KEYS"
)

// Encoding of LICENSE_PUBLIC_KEYS: "<kid>:<hex>,<kid>:<hex>". Chosen to match
// what envconfig would parse into the same field on the path that does use it,
// so a value written for one path works on the other.
const (
	publicKeysDelimiter = ","
	publicKeysSeparator = ":"
)

// writeTokenEnv carries the credential for the mutating RPCs, so it never has to
// appear in a shell history or a Taskfile.
const writeTokenEnv = "EASYP_TOKEN"

// resolveWriteToken returns the token the client authenticates with: the flag
// wins, otherwise the environment. Empty means the call goes out unauthenticated,
// which is correct for the read-only commands.
func resolveWriteToken(flagValue string) string {
	if flagValue != "" {
		return strings.TrimSpace(flagValue)
	}

	return strings.TrimSpace(os.Getenv(writeTokenEnv))
}

// licenseCredentials is what the licence client needs to reach a verdict: the
// token to check and the keys to check it against. No key at all puts the
// service in community mode.
type licenseCredentials struct {
	token string
	// publicKeys maps key id to hex-encoded Ed25519 public key. The key id in
	// the token footer selects one of these.
	publicKeys map[string]string
	// publicKey is the single-key configuration, used for tokens whose key id
	// names nothing in publicKeys.
	publicKey string
}

// resolveLicense collects the licence token and the verification keys from
// configuration, falling back to the environment for each.
func resolveLicense(cfg config.LicenseConfig) (licenseCredentials, error) {
	token, err := resolveLicenseToken(cfg)
	if err != nil {
		return licenseCredentials{}, err
	}

	return licenseCredentials{
		token:      token,
		publicKeys: resolveLicensePublicKeys(cfg),
		publicKey:  resolveLicensePublicKey(cfg),
	}, nil
}

// buildLicenseClient constructs the licence client the service runs with.
//
// A key that does not decode stops startup. Community mode is what you get for
// configuring no licence; it is not what you should get for mistyping one, and
// the difference is only visible if the second case is loud.
func buildLicenseClient(cfg config.LicenseConfig, log *slog.Logger) (*license.PasetoLicenseClient, error) {
	creds, err := resolveLicense(cfg)
	if err != nil {
		return nil, err
	}

	client, err := license.NewPasetoLicenseClient(creds.token, creds.publicKeys, creds.publicKey, log)
	if err != nil {
		return nil, fmt.Errorf("license.NewPasetoLicenseClient: %w", err)
	}

	return client, nil
}

// resolveLicenseToken returns the licence token, or an empty string when none
// is configured.
//
// Precedence: license.key, then the contents of license.file, then LICENSE_KEY
// from the environment.
func resolveLicenseToken(cfg config.LicenseConfig) (string, error) {
	if cfg.Key != "" {
		return strings.TrimSpace(cfg.Key), nil
	}

	if cfg.File != "" {
		data, err := os.ReadFile(cfg.File)
		if err != nil {
			return "", fmt.Errorf("os.ReadFile %s: %w", cfg.File, err)
		}

		return strings.TrimSpace(string(data)), nil
	}

	return strings.TrimSpace(os.Getenv(licenseTokenEnv)), nil
}

// resolveLicensePublicKey returns the single Ed25519 verification key, or an
// empty string when none is configured.
//
// Precedence: license.public_key, then LICENSE_PUBLIC_KEY from the environment.
func resolveLicensePublicKey(cfg config.LicenseConfig) string {
	if cfg.PublicKey != "" {
		return strings.TrimSpace(cfg.PublicKey)
	}

	return strings.TrimSpace(os.Getenv(licensePublicKeyEnv))
}

// resolveLicensePublicKeys returns the key id to public key map, or nil when
// none is configured.
//
// Precedence: license.public_keys, then LICENSE_PUBLIC_KEYS from the
// environment. Malformed entries are skipped here and the resulting map is
// validated by the caller; an entry that survives is still only a candidate.
func resolveLicensePublicKeys(cfg config.LicenseConfig) map[string]string {
	if len(cfg.PublicKeys) > 0 {
		keys := make(map[string]string, len(cfg.PublicKeys))
		for kid, hexKey := range cfg.PublicKeys {
			keys[strings.TrimSpace(kid)] = strings.TrimSpace(hexKey)
		}

		return keys
	}

	raw := strings.TrimSpace(os.Getenv(licensePublicKeysEnv))
	if raw == "" {
		return nil
	}

	keys := make(map[string]string)

	for pair := range strings.SplitSeq(raw, publicKeysDelimiter) {
		kid, hexKey, found := strings.Cut(strings.TrimSpace(pair), publicKeysSeparator)
		if !found {
			continue
		}

		kid, hexKey = strings.TrimSpace(kid), strings.TrimSpace(hexKey)
		if kid == "" || hexKey == "" {
			continue
		}

		keys[kid] = hexKey
	}

	if len(keys) == 0 {
		return nil
	}

	return keys
}
