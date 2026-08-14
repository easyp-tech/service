package config

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

// LoadAndValidateWith is LoadAndValidate with the environment supplied by the
// caller. Exported to the test package so the shipped configs can be checked
// against a known environment rather than whatever the shell running the test
// happens to export — otherwise a developer with REGISTRY_S3_BUCKET set would
// get a different verdict from CI.
func LoadAndValidateWith(
	ctx context.Context,
	path string,
	lookuper envconfig.Lookuper,
) (*Config, []string, error) {
	cfg, warnings, _, err := loadAndValidate(ctx, path, lookuper)

	return cfg, warnings, err
}

// LoadWithOrigins is LoadAndValidateWith, and also reports which layer supplied
// each setting.
func LoadWithOrigins(
	ctx context.Context,
	path string,
	lookuper envconfig.Lookuper,
) (*Config, Origins, error) {
	cfg, _, origins, err := loadAndValidate(ctx, path, lookuper)

	return cfg, origins, err
}

// EnvironmentOriginsWith is EnvironmentOrigins with the environment supplied by
// the caller.
func EnvironmentOriginsWith(lookuper envconfig.Lookuper) (Origins, error) {
	return environmentOrigins(lookuper)
}

// ApplyEnvWith is ApplyEnv with the environment supplied by the caller.
func ApplyEnvWith(ctx context.Context, cfg *Config, lookuper envconfig.Lookuper) error {
	return applyEnv(ctx, cfg, lookuper)
}

// EmptyIsUnset wraps a lookuper the way the real environment is wrapped, so a
// test can check that a set-but-empty variable is treated as absent.
func EmptyIsUnset(inner envconfig.Lookuper) envconfig.Lookuper {
	return emptyIsUnset{inner: inner}
}
