//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// capGate is a licence with a plugin ceiling and nothing else restricted.
type capGate struct{ maxPlugins int }

func (g capGate) Enabled(core.Feature) bool { return true }
func (g capGate) MaxWorkers() int           { return core.LicenseUnlimited }
func (g capGate) MaxPlugins() int           { return g.maxPlugins }
func (g capGate) MaxGenerations() int       { return core.LicenseUnlimited }

// TestPluginCapHoldsUnderConcurrentRegistrations is the race the licence check
// lost: it counted the registry, then inserted, so concurrent registrations all
// passed a count taken before any of them landed — eight parallel calls left
// fourteen rows under a ceiling of ten. With the count and the insert under one
// lock, exactly as many land as the ceiling has room for.
//
// Not parallel: the ceiling counts every row in the shared database, so another
// test registering at the same time would move it.
//
//nolint:paralleltest // the ceiling counts the shared database; see above
func TestPluginCapHoldsUnderConcurrentRegistrations(t *testing.T) {
	const room, attempts = 10, 25

	probe := newHarness(t, nil)
	existing, err := probe.pool.List(t.Context(), core.PluginFilter{}, core.PluginPage{})
	require.NoError(t, err)

	harness := newHarness(t, capGate{maxPlugins: len(existing) + room})

	group, name := "test", "capped"
	base := uniqueVersion()

	// t.Context is already cancelled when cleanups run.
	t.Cleanup(func() {
		for i := range attempts {
			_ = probe.pool.Delete(context.Background(), group, name, fmt.Sprintf("%s%d", base, i))
		}
	})

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		created   int
		refused   int
		unexpects []error
	)

	for i := range attempts {
		wg.Go(func() {
			_, err := harness.core.CreatePlugin(t.Context(), core.CreatePluginRequest{
				Group:   group,
				Name:    name,
				Version: fmt.Sprintf("%s%d", base, i),
				Tags:    []string{},
			})

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				created++
			case errors.Is(err, core.ErrMaxPluginsExceeded):
				refused++
			default:
				unexpects = append(unexpects, err)
			}
		})
	}

	wg.Wait()

	require.Empty(t, unexpects)
	assert.Equal(t, room, created, "exactly the room under the ceiling is used")
	assert.Equal(t, attempts-room, refused)

	after, err := probe.pool.List(t.Context(), core.PluginFilter{}, core.PluginPage{})
	require.NoError(t, err)
	assert.Len(t, after, len(existing)+room)
}
