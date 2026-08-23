package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagedRegistry serves a fixed ordered listing and records the page it was
// asked for, so the tests can see both what the core requested and what the
// caller got back.
type pagedRegistry struct {
	Registry

	items       []PluginInfo
	gotPageSize int
}

func (r *pagedRegistry) List(_ context.Context, _ PluginFilter, page PluginPage) ([]PluginInfo, error) {
	r.gotPageSize = page.Size

	start := 0
	if page.After != nil {
		for i, item := range r.items {
			if item.Group == page.After.Group && item.Name == page.After.Name && item.Version == page.After.Version {
				start = i + 1

				break
			}
		}
	}

	end := len(r.items)
	if page.Size > 0 && start+page.Size < end {
		end = start + page.Size
	}

	return r.items[start:end], nil
}

func pluginFixtures(n int) []PluginInfo {
	items := make([]PluginInfo, 0, n)
	for i := range n {
		items = append(items, PluginInfo{
			Group:   "grp",
			Name:    fmt.Sprintf("plugin-%04d", i),
			Version: "v1.0.0",
		})
	}

	return items
}

// TestListPluginsPagination pins the page walk: every page but the last is
// full and carries a continuation, the last one is short and carries none, and
// nothing is lost or repeated across the walk.
func TestListPluginsPagination(t *testing.T) {
	t.Parallel()

	reg := &pagedRegistry{items: pluginFixtures(25)}
	module := New(&countingMetrics{}, reg, enterpriseGate(), &fakeSink{}, testLogger())

	var walked []PluginInfo

	page := PluginPage{Size: 10}

	for range 4 { // 25 items in pages of 10 must finish in 3 pages
		list, err := module.ListPlugins(t.Context(), PluginFilter{}, page)
		require.NoError(t, err)

		assert.Equal(t, 11, reg.gotPageSize, "the core probes one row past the page to learn whether a next page exists")

		walked = append(walked, list.Plugins...)

		if list.Next == nil {
			break
		}

		require.Len(t, list.Plugins, 10, "every page before the last must be full")
		page.After = list.Next
	}

	assert.Equal(t, reg.items, walked, "the walk must return every item exactly once, in order")
}

func TestListPluginsPageSizeIsNormalised(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		asked     int
		wantProbe int
	}{
		{"zero selects the default", 0, DefaultPageSize + 1},
		{"negative selects the default", -5, DefaultPageSize + 1},
		{"above the ceiling is cut to it", MaxPageSize * 3, MaxPageSize + 1},
		{"in range is taken as asked", 7, 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := &pagedRegistry{items: pluginFixtures(3)}
			module := New(&countingMetrics{}, reg, enterpriseGate(), &fakeSink{}, testLogger())

			_, err := module.ListPlugins(t.Context(), PluginFilter{}, PluginPage{Size: tc.asked})
			require.NoError(t, err)

			assert.Equal(t, tc.wantProbe, reg.gotPageSize)
		})
	}
}

// TestListPluginsExactMultiple covers the boundary the probe exists for: a
// listing whose size is an exact multiple of the page ends with a full page
// and no continuation, not with an extra empty page.
func TestListPluginsExactMultiple(t *testing.T) {
	t.Parallel()

	reg := &pagedRegistry{items: pluginFixtures(10)}
	module := New(&countingMetrics{}, reg, enterpriseGate(), &fakeSink{}, testLogger())

	list, err := module.ListPlugins(t.Context(), PluginFilter{}, PluginPage{Size: 10})
	require.NoError(t, err)

	assert.Len(t, list.Plugins, 10)
	assert.Nil(t, list.Next, "a listing that fits one page exactly has no next page")
}
