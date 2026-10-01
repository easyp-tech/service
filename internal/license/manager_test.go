package license

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// stubClient reports whatever claims it is told to, so the manager can be
// observed across a change of tier without any cryptography in the way.
type stubClient struct {
	claims core.LicenseClaims
	err    error
}

func (s *stubClient) ValidateLicense(context.Context) (core.LicenseClaims, error) {
	return s.claims, s.err
}

// TestRefreshLogsOnlyOnChange guards the log volume. refresh runs once every
// CacheTTL for the lifetime of the process, so logging every result at Info
// buries the one event worth seeing — the tier actually changing — under
// identical lines. With the default five-minute TTL that is 288 a day.
func TestRefreshLogsOnlyOnChange(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := &stubClient{claims: core.EnterpriseLicenseClaims(time.Now().Add(30*24*time.Hour), false)}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		logger, prometheus.NewRegistry(), "test")
	require.NoError(t, err)

	require.Equal(t, 1, strings.Count(buf.String(), "license refreshed"),
		"the first fetch changes the tier from community, so it is worth announcing")

	for range 5 {
		manager.refresh(t.Context())
	}

	require.Equal(t, 1, strings.Count(buf.String(), "license refreshed"),
		"nothing changed, so nothing should be said again")

	// Losing the licence is exactly the event these logs exist for.
	client.claims = core.CommunityLicenseClaims()
	manager.refresh(t.Context())

	require.Equal(t, 2, strings.Count(buf.String(), "license refreshed"))
	require.Equal(t, core.LicenseTierCommunity, manager.Claims().Tier)
}

func TestRefreshKeepsPreviousClaimsOnError(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	client := &stubClient{claims: core.EnterpriseLicenseClaims(time.Now().Add(30*24*time.Hour), false)}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		logger, prometheus.NewRegistry(), "test")
	require.NoError(t, err)
	require.Equal(t, core.LicenseTierEnterprise, manager.Claims().Tier)

	client.err = ErrNoClient
	manager.refresh(t.Context())

	require.Equal(t, core.LicenseTierEnterprise, manager.Claims().Tier,
		"a failed refresh must not downgrade a licence that was valid a moment ago")
}

// TestCeilingChangeWarnsThatPoolNeedsRestart pins the one signal an operator
// gets that the worker pool did not follow a tier change. The pool reads its
// ceilings once at startup; a licence lapsing mid-run otherwise drops capacity
// silently on the next restart.
func TestCeilingChangeWarnsThatPoolNeedsRestart(t *testing.T) {
	t.Parallel()

	const warning = "applies the new ones on restart"

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := &stubClient{claims: core.EnterpriseLicenseClaims(time.Now().Add(30*24*time.Hour), false)}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		logger, prometheus.NewRegistry(), "test")
	require.NoError(t, err)

	require.NotContains(t, buf.String(), warning,
		"the first fetch is what the pool is sized from, so there is nothing to warn about")

	client.claims = core.CommunityLicenseClaims()
	manager.refresh(t.Context())
	manager.refresh(t.Context())

	require.Equal(t, 1, strings.Count(buf.String(), warning),
		"one warning per transition, not one per refresh")
}

// TestGenerationsCeilingAloneCountsAsChange covers the field the change check
// used to skip: a licence differing only in MaxGenerations refreshed at Debug,
// so a change to the ceiling that bounds throughput went unannounced.
func TestGenerationsCeilingAloneCountsAsChange(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	claims := core.CommunityLicenseClaims()
	client := &stubClient{claims: claims}

	manager, err := NewManager(t.Context(), client, Config{CacheTTL: time.Hour},
		logger, prometheus.NewRegistry(), "test")
	require.NoError(t, err)

	before := strings.Count(buf.String(), "license refreshed")

	claims.MaxGenerations++
	client.claims = claims
	manager.refresh(t.Context())

	require.Equal(t, before+1, strings.Count(buf.String(), "license refreshed"))
}
