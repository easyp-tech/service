package telemetry_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/telemetry"
)

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

	handler := slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}) //nolint:exhaustruct

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
