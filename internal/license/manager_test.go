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
