package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	generator "github.com/easyp-tech/service/api/easyp/generator/v1"
)

// pagedPlugins serves a fixed number of pages, one plugin each, and spends
// delay on every call. It records the deadline it was handed so a test can tell
// a per-page budget from a per-traversal one.
type pagedPlugins struct {
	generator.GeneratorAPIClient

	pages     int
	delay     time.Duration
	served    int
	deadlines []time.Duration
}

func (p *pagedPlugins) Plugins(
	ctx context.Context, _ *generator.PluginsRequest, _ ...grpc.CallOption,
) (*generator.PluginsResponse, error) {
	if deadline, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, time.Until(deadline))
	}

	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	p.served++

	resp := &generator.PluginsResponse{
		Plugins: []*generator.PluginInfo{{Name: "plugin"}},
	}

	if p.served < p.pages {
		token := "next"
		resp.NextPageToken = token
	}

	return resp, nil
}

func testClient(t *testing.T, fake generator.GeneratorAPIClient, timeout time.Duration) *Client {
	t.Helper()

	cfg := defaultConfig()
	cfg.listPluginsTimeout = timeout

	return &Client{genClient: fake, cfg: cfg} //nolint:exhaustruct // no connection is needed
}

// TestListPluginsBudgetsEachPage is the regression guard for a walk that could
// not finish.
//
// The timeout was applied once, around the whole loop, so it was a budget for
// the entire registry rather than for a request. A registry with more pages
// than the budget covers could not be listed at all — and the plugin count is
// uncapped on exactly the tier most likely to have one.
func TestListPluginsBudgetsEachPage(t *testing.T) {
	t.Parallel()

	// Four pages at 40ms each is 160ms of work under a 100ms per-page budget:
	// impossible if the budget spans the traversal, comfortable if it does not.
	fake := &pagedPlugins{pages: 4, delay: 40 * time.Millisecond} //nolint:exhaustruct // counters start at zero

	plugins, err := testClient(t, fake, 100*time.Millisecond).ListPlugins(t.Context())

	require.NoError(t, err)
	require.Len(t, plugins, 4)
	require.Equal(t, 4, fake.served)

	for i, remaining := range fake.deadlines {
		require.Greater(t, remaining, 50*time.Millisecond,
			"page %d started with a shrunken budget, so the timeout still spans the walk", i)
	}
}

// TestListPluginsHonoursTheCallersDeadline keeps the fix from removing the only
// bound on the whole walk: a caller that wants one sets it on the context, and
// withTimeout takes the earlier of the two.
func TestListPluginsHonoursTheCallersDeadline(t *testing.T) {
	t.Parallel()

	fake := &pagedPlugins{pages: 100, delay: 20 * time.Millisecond} //nolint:exhaustruct // counters start at zero

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()

	_, err := testClient(t, fake, time.Minute).ListPlugins(ctx)

	require.Error(t, err)
	require.Less(t, fake.served, 100, "the caller's deadline must stop the walk")
}

// TestListPluginsStopsWhenTheCallerCancels covers the gap between pages: a
// cancelled caller should not have another page started on its behalf.
func TestListPluginsStopsWhenTheCallerCancels(t *testing.T) {
	t.Parallel()

	fake := &pagedPlugins{pages: 100, delay: time.Millisecond} //nolint:exhaustruct // counters start at zero

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := testClient(t, fake, time.Minute).ListPlugins(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, fake.served, 100)
}
