package telemetry_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/telemetry"
)

// TestEndpointFormPicksTheExporterOption pins which endpoint spellings go
// through WithEndpointURL rather than WithEndpoint. The standard
// OTEL_EXPORTER_OTLP_ENDPOINT variable (read through the env alias) carries a
// URL with a scheme, which WithEndpoint would use verbatim as a hostname — the
// exporter would then retry a nonexistent host forever.
func TestEndpointFormPicksTheExporterOption(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		endpoint string
		isURL    bool
	}{
		{"bare host and port", "easyp-alloy:4317", false},
		{"bare host", "localhost", false},
		{"http URL, as the OTel operator injects it", "http://otel-collector:4317", true},
		{"https URL", "https://collector.example.com:4317", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.isURL, telemetry.EndpointIsURL(tc.endpoint))
		})
	}
}

// TestInitWithoutEndpointsBuildsNoExporters pins the behaviour the empty
// endpoint is for.
//
// It cannot be asserted by watching for a failure, because there is none to
// watch for: the OTLP exporter connects lazily, so pointing it at an address
// nobody answers on succeeds here and only shows up later as an export retried
// forever. The observable difference is in what Init says it did, so that is
// what this checks — a stack brought up without a collector must report that it
// is not exporting rather than quietly arranging to fail on a timer.
func TestInitWithoutEndpointsBuildsNoExporters(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	handler := slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo})

	shutdown, logger, err := telemetry.Init(t.Context(), telemetry.Config{
		OTLPEndpoint:      "",
		ServiceName:       "easyp",
		PyroscopeEndpoint: "",
	}, handler)
	require.NoError(t, err)
	require.NotNil(t, logger, "the service still needs a logger with no telemetry configured")
	require.NotNil(t, shutdown)

	out := log.String()
	require.Contains(t, out, "no OTLP endpoint configured")
	require.Contains(t, out, "no Pyroscope endpoint configured")

	// Shutting down what was never started must not report an error.
	require.NoError(t, shutdown(t.Context()))
}
