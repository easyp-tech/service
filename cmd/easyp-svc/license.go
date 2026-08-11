package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/easyp-tech/service/internal/config"
	"github.com/easyp-tech/service/internal/license"
)

// The licence and storage settings used to be read from the environment here by
// hand, because the --cfg path decoded YAML and skipped envconfig entirely. It
// no longer does: config.LoadAndValidate overlays the environment onto the file
// on both paths, so LICENSE_KEY, LICENSE_PUBLIC_KEY, LICENSE_PUBLIC_KEYS and the
// REGISTRY_S3_* pair arrive in the config like every other field. What is left
// below is only what envconfig cannot do: reading a token out of a file, and the
// client-side flag fallback.

// writeTokenEnv carries the credential for the mutating RPCs, so it never has to
// appear in a shell history or a Taskfile.
//
// Unlike the settings above this is not a config field: it belongs to the client
// commands, which authenticate as a caller rather than being configured as a
// server.
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

// resolveLicense collects the licence token and the verification keys from the
// configuration, which by this point already carries whatever the environment
// supplied.
func resolveLicense(cfg config.LicenseConfig) (licenseCredentials, error) {
	token, err := resolveLicenseToken(cfg)
	if err != nil {
		return licenseCredentials{}, err
	}

	return licenseCredentials{
		token:      token,
		publicKeys: trimmedPublicKeys(cfg),
		publicKey:  strings.TrimSpace(cfg.PublicKey),
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
// Precedence: license.key, then the contents of license.file. The key itself may
// have come from LICENSE_KEY; the file is the part envconfig cannot express,
// which is why this function still exists.
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

	return "", nil
}

// trimmedPublicKeys returns the key id to public key map with the surrounding
// whitespace removed, or nil when none is configured.
//
// The trimming is not decoration: these arrive either from YAML, where a value
// may be wrapped across lines, or from LICENSE_PUBLIC_KEYS, where a space after
// a comma is the natural way to write a list. A key with a stray space fails to
// decode as hex, and the service falls back to community mode over a typo that
// is invisible in the file.
func trimmedPublicKeys(cfg config.LicenseConfig) map[string]string {
	if len(cfg.PublicKeys) == 0 {
		return nil
	}

	keys := make(map[string]string, len(cfg.PublicKeys))

	for kid, hexKey := range cfg.PublicKeys {
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
