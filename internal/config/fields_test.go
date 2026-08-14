package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// TestLeavesCoverEveryField is the guarantee the rest of the package leans on:
// the walk either names a field or refuses to run. A field with no yaml tag or
// no env key is a setting reachable through one door and not the other, which is
// invisible until someone tries the door that is not there — so the walk returns
// an error rather than quietly skipping it.
func TestLeavesCoverEveryField(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err, "every field of Config must carry both a yaml and an env tag")
	require.NotEmpty(t, leaves)

	for _, leaf := range leaves {
		require.NotEmpty(t, leaf.EnvKey, "%s has no environment variable", leaf.Name())
		require.NotEmpty(t, leaf.YAMLPath, "%s has no place in the config file", leaf.EnvKey)
		require.NotEmpty(t, leaf.Index, "%s does not locate a field", leaf.Name())
	}
}

// TestLeafNamesAreUnique guards the assumption both the loader and `config
// print` make when they key a map by one of these names. A collision would not
// fail anything loudly: one setting would silently take the other's origin, or
// overwrite its value during the merge.
func TestLeafNamesAreUnique(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	envKeys := make(map[string]string, len(leaves))
	yamlPaths := make(map[string]string, len(leaves))

	for _, leaf := range leaves {
		previous, clash := envKeys[leaf.EnvKey]
		require.False(t, clash, "%s and %s both read %s", previous, leaf.Name(), leaf.EnvKey)
		envKeys[leaf.EnvKey] = leaf.Name()

		previous, clash = yamlPaths[leaf.Name()]
		require.False(t, clash, "%s is claimed by both %s and %s", leaf.Name(), previous, leaf.EnvKey)
		yamlPaths[leaf.Name()] = leaf.EnvKey
	}
}

// TestLeavesMatchTheDeployedNames pins the two names against what is written
// down outside this repository's Go code: the environment variables in
// deploy/charts/easyp-service/templates/deployment.yaml and the keys in
// deploy/config/*.yml. If the prefix accumulation were wrong — a missing
// underscore, a section skipped — every one of those files would be addressing a
// setting that does not exist, and nothing else would say so.
func TestLeavesMatchTheDeployedNames(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	byName := make(map[string]config.Leaf, len(leaves))
	for _, leaf := range leaves {
		byName[leaf.Name()] = leaf
	}

	cases := []struct {
		yamlPath   string
		envKey     string
		defaultVal string
		hasDefault bool
		secret     bool
	}{
		// Two levels of prefix, which is where an accumulation bug would show.
		{"server.port.grpc", "SERVER_PORT_GRPC", "23410", true, false},
		{"registry.s3.bucket", "REGISTRY_S3_BUCKET", "", false, false},
		// The default the compose configs and the chart disagreed about.
		{"worker_pool.max_retries", "WORKER_POOL_MAX_RETRIES", "2", true, false},
		// A default containing a character that the tag parser splits on
		// elsewhere would be truncated by a naive parse.
		{"registry.s3.region", "REGISTRY_S3_REGION", "us-east-1", true, false},
		// Deliberately without a default: a fallback would make "no collector"
		// impossible to express. See TestTelemetryHasNoDefault.
		{"telemetry.otlp_endpoint", "TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT", "", false, false},
		// The three credentials, and the identifier that is deliberately not one.
		{"db.postgres", "DB_POSTGRES_DSN", "", false, true},
		{"registry.s3.secret_access_key", "REGISTRY_S3_SECRET_ACCESS_KEY", "", false, true},
		{"registry.s3.access_key_id", "REGISTRY_S3_ACCESS_KEY_ID", "", false, false},
		{"license.key", "LICENSE_KEY", "", false, true},
		// A slice with a custom decoder is one setting, not one per entry.
		{"auth.write_tokens", "AUTH_WRITE_TOKENS", "", false, false},
		{"license.public_keys", "LICENSE_PUBLIC_KEYS", "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.yamlPath, func(t *testing.T) {
			t.Parallel()

			leaf, found := byName[tc.yamlPath]
			require.True(t, found, "no leaf named %s", tc.yamlPath)

			require.Equal(t, tc.envKey, leaf.EnvKey)
			require.Equal(t, tc.hasDefault, leaf.HasDefault)
			require.Equal(t, tc.defaultVal, leaf.Default)
			require.Equal(t, tc.secret, leaf.Secret)
		})
	}
}

// TestSecretsAreMarked states the whole list in one place. It is a decision, not
// a derivation: adding a field that holds a credential must be a deliberate
// choice to mark it, and this test is what makes forgetting visible.
func TestSecretsAreMarked(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	var secrets []string

	for _, leaf := range leaves {
		if leaf.Secret {
			secrets = append(secrets, leaf.Name())
		}
	}

	require.ElementsMatch(t, []string{
		"db.postgres",
		"registry.s3.secret_access_key",
		"license.key",
	}, secrets, "a new credential in the config must be marked `secret:\"true\"` or it will be printed")
}

// TestLeafValueAddressesTheRightField covers the index path, which is the one
// part of a Leaf that cannot be wrong in a way the other tests would notice: a
// leaf could carry the right names and still point at the neighbouring field.
func TestLeafValueAddressesTheRightField(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	cfg := config.Config{} //nolint:exhaustruct // two fields under test
	cfg.Registry.S3.Bucket = "plugins"
	cfg.WorkerPool.MaxRetries = 7

	for _, leaf := range leaves {
		switch leaf.Name() {
		case "registry.s3.bucket":
			require.Equal(t, "plugins", leaf.Value(&cfg).String())
		case "worker_pool.max_retries":
			require.Equal(t, int64(7), leaf.Value(&cfg).Int())
		}
	}
}
