//go:build integration

package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLatestResolvesToTheNewestVersion goes through the real query. `latest`
// used to be `order by version desc` on a text column, which put v1.36.9 above
// v1.36.11 and served the older release.
func TestLatestResolvesToTheNewestVersion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t, nil)

	// A name of its own, since runs share one database and other tests register
	// under "test" with versions of their own.
	group, name := "test", fmt.Sprintf("latest-%d", time.Now().UnixNano()%1_000_000_000)

	binary := buildStubPlugin(t, harness, group, name, "v1.36.11")
	for _, version := range []string{"v1.36.9", "v1.36.11", "v1.36.10"} {
		registerPlugin(t, harness, group, name, version, binary)
	}

	plugin, err := harness.repo.Get(t.Context(), group, name, "latest")
	require.NoError(t, err)
	require.Equal(t, "v1.36.11", plugin.Info(t.Context()).Version)
}
