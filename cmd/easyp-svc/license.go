package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/easyp-tech/service/internal/config"
)

// These variables are consulted directly because the --cfg path decodes YAML
// and skips envconfig entirely, so licence settings handed to the container
// through the environment would otherwise be dropped on the floor.
const (
	licenseTokenEnv     = "LICENSE_KEY"
	licensePublicKeyEnv = "LICENSE_PUBLIC_KEY"
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
// token to check and the key to check it against. Either being empty puts the
// service in community mode.
type licenseCredentials struct {
	token     string
	publicKey string
}

// resolveLicense collects the licence token and the verification key from
// configuration, falling back to the environment for each.
func resolveLicense(cfg config.LicenseConfig) (licenseCredentials, error) {
	token, err := resolveLicenseToken(cfg)
	if err != nil {
		return licenseCredentials{}, err //nolint:exhaustruct // the error is the result
	}

	return licenseCredentials{
		token:     token,
		publicKey: resolveLicensePublicKey(cfg),
	}, nil
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

// resolveLicensePublicKey returns the Ed25519 verification key, or an empty
// string when none is configured.
//
// Precedence: license.public_key, then LICENSE_PUBLIC_KEY from the environment.
func resolveLicensePublicKey(cfg config.LicenseConfig) string {
	if cfg.PublicKey != "" {
		return strings.TrimSpace(cfg.PublicKey)
	}

	return strings.TrimSpace(os.Getenv(licensePublicKeyEnv))
}
