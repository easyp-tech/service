package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLogLevelAccepted(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"INFO":  slog.LevelInfo,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lvl, err := parseLogLevel(name)
			require.NoError(t, err)
			require.Equal(t, want, lvl)
		})
	}
}

// A value nobody meant must not turn the loudest setting on. Debug logs every
// call this service handles, and its calls carry other people's source; a typo
// is not consent to that. The error is what the caller reports, so the fallback
// stays distinguishable from an explicit --log_level=info.
func TestParseLogLevelFallsBackToInfoNotDebug(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "trace", "verbose", "DEGUB", "9"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			lvl, err := parseLogLevel(bad)
			require.Error(t, err)
			require.Equal(t, slog.LevelInfo, lvl)
			require.NotEqual(t, slog.LevelDebug, lvl)
		})
	}
}
