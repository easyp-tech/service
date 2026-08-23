//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/core"
)

// TestListPluginsPaginatesAgainstPostgres walks a real keyset-paginated
// listing. The unit tests pin the paging arithmetic against a stub; this is
// the only place the actual SQL — the row comparison, the ORDER BY, the LIMIT
// — runs against the database that has to honour it.
func TestListPluginsPaginatesAgainstPostgres(t *testing.T) {
	t.Parallel()

	h := newHarness(t, nil)

	// The database persists across runs, so the test filters to a group only
	// it writes to.
	group := fmt.Sprintf("pgtest%d", time.Now().UnixNano()%1_000_000_000)

	const total = 7

	want := make([]string, 0, total)

	// The command only has to point inside the plugins directory to pass
	// registration; listing never executes it.
	binary := filepath.Join(h.pluginsDir, "stub")

	for i := range total {
		name := fmt.Sprintf("plugin-%02d", i)
		registerPlugin(t, h, group, name, "v1.0.0", binary)
		want = append(want, name)
	}

	filter := core.PluginFilter{Group: group}

	var got []string

	page := core.PluginPage{Size: 3}

	pages := 0
	for {
		pages++
		require.LessOrEqual(t, pages, 4, "7 rows in pages of 3 must finish in 3 pages")

		list, err := h.core.ListPlugins(t.Context(), filter, page)
		require.NoError(t, err)

		for _, p := range list.Plugins {
			assert.Equal(t, group, p.Group, "the filter must hold on every page")
			got = append(got, p.Name)
		}

		if list.Next == nil {
			assert.Len(t, list.Plugins, 1, "the last page holds the 7th row and nothing else")

			break
		}

		require.Len(t, list.Plugins, 3, "every page before the last must be full")
		page.After = list.Next
	}

	assert.Equal(t, want, got, "the walk must see every row exactly once, in (group, name, version) order")
}
