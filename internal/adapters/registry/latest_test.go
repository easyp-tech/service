package registry

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewestVersion pins `latest` to numeric order. The first cases are what
// plugins.easyp.tech actually served on 2026-11-17, when the newest version was
// chosen by sorting strings: v1.36.9 for protocolbuffers/go while v1.36.11 was
// registered, and the same for eight other plugins.
func TestNewestVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{"patch past nine", []string{"v1.36.9", "v1.36.10", "v1.36.11"}, "v1.36.11"},
		{"minor past nine", []string{"v1.9.9", "v1.26.0"}, "v1.26.0"},
		{"minor past nine, small patch", []string{"v2.9.0", "v2.12.0"}, "v2.12.0"},
		{"zero major", []string{"v0.9.1", "v0.13.0"}, "v0.13.0"},
		{"two-part protobuf releases", []string{"v3.14.0", "v29.3", "v33.1", "v35.0"}, "v35.0"},
		{"major past nine", []string{"v9.0", "v10.0"}, "v10.0"},
		{"missing patch is zero", []string{"v33.0.1", "v33.1"}, "v33.1"},
		{"equal numbers fall back to the string", []string{"v33.1.0", "v33.1"}, "v33.1.0"},
		{"one version", []string{"v1.0.0"}, "v1.0.0"},
		{"unparsable ranks last", []string{"latest-ish", "v0.0.1"}, "v0.0.1"},
		{"only unparsable", []string{"b", "a"}, "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, newestVersion(tt.versions))

			// The database returns rows in no promised order, so neither may
			// the answer depend on it.
			for range 20 {
				shuffled := append([]string(nil), tt.versions...)
				rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
				assert.Equal(t, tt.want, newestVersion(shuffled), "order %v", shuffled)
			}
		})
	}
}

func TestVersionParts(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "v", "1.2.3", "v1", "v1.2.3.4", "v1..2", "v1.+2", "v1.-2", "v1.x", "vlatest"} {
		_, ok := versionParts(bad)
		assert.False(t, ok, "%q must not parse", bad)
	}

	parts, ok := versionParts("v33.1")
	assert.True(t, ok)
	assert.Equal(t, [3]int{33, 1, 0}, parts)
}
