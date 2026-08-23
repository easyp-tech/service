package config_test

import (
	"testing"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/config"
)

// TestStandardOTelNameIsRead covers the rename's other half. The endpoint's
// variable was TELEMETRY_OTEL_EXPORTER_OTLP_ENDPOINT — a name that looked like
// the OpenTelemetry SDK's own and was not it, while the real one was read by
// nothing. Someone who had configured a collector before got a service with no
// traces and no error.
func TestStandardOTelNameIsRead(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, config.AliasLookuperFor(
		envconfig.MapLookuper(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317"}),
	)))

	require.Equal(t, "collector:4317", cfg.Telemetry.OTLPEndpoint)
}

// TestCanonicalNameWinsOverAlias pins which one is in charge. An operator who
// sets both has said something specific with the service's own variable.
func TestCanonicalNameWinsOverAlias(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, config.AliasLookuperFor(
		envconfig.MapLookuper(map[string]string{
			"TELEMETRY_OTLP_ENDPOINT":     "ours:4317",
			"OTEL_EXPORTER_OTLP_ENDPOINT": "theirs:4317",
		}),
	)))

	require.Equal(t, "ours:4317", cfg.Telemetry.OTLPEndpoint)
}

// TestAliasIsReported is what keeps `config print --origin` honest. Naming the
// canonical variable for a value that arrived under the alternative one would
// send the reader to a variable that is not set — the precise failure the
// command exists to prevent.
func TestAliasIsReported(t *testing.T) {
	t.Parallel()

	lookuper := config.AliasLookuperFor(
		envconfig.MapLookuper(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317"}),
	)

	cfg := config.Config{}
	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, lookuper))

	require.Equal(t, map[string]string{"TELEMETRY_OTLP_ENDPOINT": "OTEL_EXPORTER_OTLP_ENDPOINT"},
		config.AliasesUsed(lookuper))
}

// TestEmptyAliasIsUnset keeps the alias consistent with every other variable:
// compose writes `"${VAR:-}"`, and a defined-but-empty one must not count.
func TestEmptyAliasIsUnset(t *testing.T) {
	t.Parallel()

	cfg := config.Config{}

	require.NoError(t, config.ApplyEnvWith(t.Context(), &cfg, config.AliasLookuperFor(
		config.EmptyIsUnset(envconfig.MapLookuper(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": ""})),
	)))

	require.Empty(t, cfg.Telemetry.OTLPEndpoint)
}
