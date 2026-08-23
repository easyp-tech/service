package config

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

// LoadWith is Load with the environment supplied by the caller. Exported to the
// test package so the shipped configs can be checked against a known
// environment rather than whatever the shell running the test happens to
// export — otherwise a developer with REGISTRY_S3_BUCKET set would get a
// different verdict from CI.
func LoadWith(ctx context.Context, path string, lookuper envconfig.Lookuper) (Result, error) {
	return load(ctx, path, lookuper)
}

// LoadFromEnvWith is LoadFromEnv with the environment supplied by the caller.
func LoadFromEnvWith(ctx context.Context, lookuper envconfig.Lookuper) (Result, error) {
	return loadFromEnv(ctx, lookuper)
}

// EnvironmentOriginsWith reports the origins of an environment-only resolution,
// with the environment supplied by the caller.
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

// AliasLookuper wraps a lookuper the way the real environment is wrapped, so a
// test can check that an alternative variable name is read, and read back which
// names actually supplied a value.
type AliasLookuper = aliasLookuper

// AliasLookuperFor wraps inner in the alias resolution the real environment gets.
func AliasLookuperFor(inner envconfig.Lookuper) *AliasLookuper {
	return newAliasLookuper(inner)
}

// AliasesUsed reports the alternative names that supplied a value.
func AliasesUsed(lookuper *AliasLookuper) map[string]string {
	return lookuper.used
}
