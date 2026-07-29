package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRegistryUnavailable = errors.New("registry unavailable")

// fakeSink is an in-memory AuditSink used in tests.
type fakeSink struct {
	mu      sync.Mutex
	entries []AuditEntry
	skipped int
}

func (s *fakeSink) Send(_ context.Context, entry AuditEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, entry)
}

func (s *fakeSink) Skipped() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.skipped++
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.entries)
}

func (s *fakeSink) skippedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.skipped
}

// fakeGate is an in-memory FeatureGate whose enabled set is fixed per test.
type fakeGate struct {
	enabled map[Feature]bool
}

func (g fakeGate) Enabled(feature Feature) bool { return g.enabled[feature] }
func (g fakeGate) MaxWorkers() int              { return -1 }
func (g fakeGate) MaxPlugins() int              { return -1 }

// failingRegistry fails every call, which drives Core down its audit-on-error paths.
type failingRegistry struct{ Registry }

func (failingRegistry) List(_ context.Context, _ PluginFilter) ([]PluginInfo, error) {
	return nil, errRegistryUnavailable
}

func enterpriseGate() fakeGate {
	return fakeGate{enabled: map[Feature]bool{
		FeatureAudit:         true,
		FeaturePluginListing: true,
		FeaturePluginCRUD:    true,
	}}
}

func communityGate() fakeGate {
	// Mirrors CommunityLicenseClaims: everything except the Enterprise features.
	return fakeGate{enabled: map[Feature]bool{
		FeatureAudit:         false,
		FeaturePluginListing: true,
		FeaturePluginCRUD:    true,
	}}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestAuditIsGatedByLicense(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		gate        FeatureGate
		wantEntries int
		wantSkipped int
	}{
		{
			name:        "enterprise_records_the_event",
			gate:        enterpriseGate(),
			wantEntries: 1,
			wantSkipped: 0,
		},
		{
			name:        "community_records_nothing",
			gate:        communityGate(),
			wantEntries: 0,
			wantSkipped: 1,
		},
		{
			name:        "nil_gate_allows_everything",
			gate:        nil,
			wantEntries: 1,
			wantSkipped: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sink := &fakeSink{}
			module := New(nil, failingRegistry{}, tt.gate, sink, testLogger())

			_, err := module.ListPlugins(t.Context(), PluginFilter{})
			require.Error(t, err)

			assert.Equal(t, tt.wantEntries, sink.count(),
				"audit entries reaching the sink")
			assert.Equal(t, tt.wantSkipped, sink.skippedCount(),
				"events skipped because the licence does not include audit")
		})
	}
}

func TestAuditErrorEntryCarriesErrorCode(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	module := New(nil, failingRegistry{}, enterpriseGate(), sink, testLogger())

	_, err := module.ListPlugins(t.Context(), PluginFilter{})
	require.Error(t, err)

	require.Equal(t, 1, sink.count())

	entry := sink.entries[0]
	assert.Equal(t, OperationListPlugins, entry.OperationType)
	assert.Equal(t, AuditStatusError, entry.Status)
	assert.NotEmpty(t, entry.ErrorCode)
	assert.NotEmpty(t, entry.ErrorMessage)
}
