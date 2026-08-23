package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// TestUnknownEnvFlagsTypo covers the layer that used to give no signal at all.
// A mistyped key in the file has been named since the strict walk; a mistyped
// variable simply did nothing, and the environment is how Helm delivers every
// secret.
func TestUnknownEnvFlagsTypo(t *testing.T) {
	t.Parallel()

	diags, err := config.UnknownEnv([]string{
		"SERVER_PORT_GRPCC=7777",
		"WORKER_POOL_WORKER=99",
	})
	require.NoError(t, err)
	require.Len(t, diags, 2)

	require.Equal(t, "SERVER_PORT_GRPCC", diags[0].Path)
	require.Equal(t, "did you mean SERVER_PORT_GRPC?", diags[0].Hint)
	require.Equal(t, config.SeverityWarning, diags[0].Severity,
		"an unrecognised variable must never refuse to start: see the link-variable case")

	require.Equal(t, "did you mean WORKER_POOL_WORKERS?", diags[1].Hint)
}

// TestUnknownEnvIgnoresKubernetesLinkVars is why these are warnings and why the
// shapes are filtered.
//
// Kubelet injects a set of variables for every Service in the namespace. A
// Service named `db` produces DB_PORT, DB_SERVICE_HOST and DB_PORT_5432_TCP_ADDR,
// all of which carry this service's DB_ section prefix and none of which anyone
// wrote. Reporting them would train the reader to ignore this warning, which is
// the same as not having it.
func TestUnknownEnvIgnoresKubernetesLinkVars(t *testing.T) {
	t.Parallel()

	diags, err := config.UnknownEnv([]string{
		"DB_PORT=tcp://10.0.0.1:5432",
		"DB_SERVICE_HOST=10.0.0.1",
		"DB_SERVICE_PORT=5432",
		"DB_PORT_5432_TCP_ADDR=10.0.0.1",
		"DB_PORT_5432_TCP_PROTO=tcp",
		"AUDIT_SERVICE_HOST=10.0.0.2",
		"LICENSE_SERVICE_PORT_HTTPS=443",
	})
	require.NoError(t, err)
	require.Empty(t, diags, "a namespace holding a Service named db must not produce warnings")
}

// TestObservabilityStackVarsAreOutOfScope covers the collision that made this
// check hard to get right.
//
// Mimir, Loki and Tempo read their object-store settings from the environment
// through -config.expand-env=true, and they used to be called TELEMETRY_S3_*,
// which is this service's section prefix. They needed an allowlist to avoid
// being reported. Renaming them to OBS_* in v0.13.0 removed the ambiguity
// instead of papering over it: nothing under TELEMETRY_ now belongs to another
// program.
func TestObservabilityStackVarsAreOutOfScope(t *testing.T) {
	t.Parallel()

	diags, err := config.UnknownEnv([]string{
		"OBS_S3_URL=http://rustfs:9000",
		"OBS_S3_ENDPOINT=rustfs:9000",
		"OBS_S3_ACCESS_KEY_ID=key",
		"OBS_S3_SECRET_ACCESS_KEY=secret",
		"OBS_S3_INSECURE=true",
		"OBS_BUCKET_PREFIX=easyp",
	})
	require.NoError(t, err)
	require.Empty(t, diags, "these carry no section prefix of this service")

	// And the old names, which no longer belong to anything, are now reported
	// rather than allowlisted — an operator with a stale deploy/.env.dev is
	// told the variable stopped doing anything.
	stale, err := config.UnknownEnv([]string{"TELEMETRY_S3_ENDPOINT=rustfs:9000"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
}

// TestUnknownEnvIgnoresEmptyAndUnprefixed covers the two remaining ways a
// variable is none of this service's business. Compose defines every optional
// secret as `"${VAR:-}"`, so an empty value is the normal state of half the
// environment and is treated as unset everywhere else in this package.
func TestUnknownEnvIgnoresEmptyAndUnprefixed(t *testing.T) {
	t.Parallel()

	diags, err := config.UnknownEnv([]string{
		"REGISTRY_S3_BUCKET=",
		"PATH=/usr/bin",
		"HOME=/root",
		"EASYP_TOKEN=abc",
	})
	require.NoError(t, err)
	require.Empty(t, diags)
}

// TestEveryLeafEnvKeyIsAccepted is the durable guard. Every real variable has to
// pass the check unremarked, or adding a setting starts producing a warning
// telling the operator that the setting they just configured does nothing.
func TestEveryLeafEnvKeyIsAccepted(t *testing.T) {
	t.Parallel()

	leaves, err := config.Leaves()
	require.NoError(t, err)

	environ := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		environ = append(environ, leaf.EnvKey+"=some-value")
	}

	diags, err := config.UnknownEnv(environ)
	require.NoError(t, err)
	require.Empty(t, diags)
}
