package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/easyp-tech/service/internal/config"
)

// licenseTokenEnv is consulted directly because the --cfg path decodes YAML and
// skips envconfig entirely, so a token handed to the container through the
// environment would otherwise be dropped on the floor.
const licenseTokenEnv = "LICENSE_KEY"

// resolveLicenseToken returns the licence token, or an empty string when none
// is configured — which puts the service in community mode.
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
