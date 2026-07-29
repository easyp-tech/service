package audit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPartitionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			name: "month_start",
			in:   time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			want: "audit_log_y2026m07",
		},
		{
			name: "mid_month_normalises_to_first",
			in:   time.Date(2026, time.July, 29, 23, 59, 59, 0, time.UTC),
			want: "audit_log_y2026m07",
		},
		{
			name: "december_pads_month",
			in:   time.Date(2025, time.December, 31, 12, 0, 0, 0, time.UTC),
			want: "audit_log_y2025m12",
		},
		{
			name: "non_utc_is_converted_not_truncated_locally",
			// 2026-08-01 01:00 +03:00 is 2026-07-31 22:00 UTC, so it belongs to July.
			in:   time.Date(2026, time.August, 1, 1, 0, 0, 0, time.FixedZone("MSK", 3*60*60)),
			want: "audit_log_y2026m07",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, partitionName(tt.in))
		})
	}
}

func TestParsePartitionMonth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  time.Time
		wantB bool
	}{
		{
			name:  "generated_partition",
			in:    "audit_log_y2026m07",
			want:  time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			wantB: true,
		},
		{
			name:  "default_partition_is_never_matched",
			in:    "audit_log_default",
			wantB: false,
		},
		{
			name:  "hand_made_partition_is_ignored",
			in:    "audit_log_archive",
			wantB: false,
		},
		{
			name:  "parent_table",
			in:    "audit_log",
			wantB: false,
		},
		{
			name:  "month_out_of_range",
			in:    "audit_log_y2026m13",
			wantB: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parsePartitionMonth(tt.in)
			require.Equal(t, tt.wantB, ok)

			if tt.wantB {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestPartitionNameRoundTrip(t *testing.T) {
	t.Parallel()

	start := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	for i := range 36 {
		month := start.AddDate(0, i, 0)

		parsed, ok := parsePartitionMonth(partitionName(month))
		require.True(t, ok, "generated name must parse back")
		assert.Equal(t, month, parsed)
	}
}

func TestUpcomingMonths(t *testing.T) {
	t.Parallel()

	// A month end is the risky input: naive AddDate on the 31st skips months.
	now := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)

	got := upcomingMonths(now, 3)

	require.Len(t, got, 4)
	assert.Equal(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), got[0])
	assert.Equal(t, time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC), got[1])
	assert.Equal(t, time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), got[2])
	assert.Equal(t, time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC), got[3])
}

func TestRetentionCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

	assert.Equal(t,
		time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC),
		retentionCutoff(now, 12),
	)
}

func TestExpiredPartitions(t *testing.T) {
	t.Parallel()

	names := []string{
		"audit_log_y2025m05",
		"audit_log_y2025m06",
		"audit_log_y2025m07",
		"audit_log_y2026m07",
		"audit_log_default",
		"audit_log_archive",
	}

	cutoff := time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC)

	got := expiredPartitions(names, cutoff)

	// The cutoff month itself is kept, and neither the default nor a hand-made
	// partition is ever selected.
	assert.Equal(t, []string{"audit_log_y2025m05", "audit_log_y2025m06"}, got)
}

func TestMonthlyPartitionsExcludesDefault(t *testing.T) {
	t.Parallel()

	names := []string{"audit_log_y2026m07", "audit_log_default", "audit_log_archive"}

	assert.Equal(t, []string{"audit_log_y2026m07"}, monthlyPartitions(names))
}

func TestPartitionConfigSetDefault(t *testing.T) {
	t.Parallel()

	// The YAML config path bypasses envconfig defaults, so a missing audit
	// block arrives as all-zero. A zero Interval would panic time.NewTicker.
	cfg := PartitionConfig{}
	cfg.setDefault()

	assert.Equal(t, defaultPreCreateMonths, cfg.PreCreateMonths)
	assert.Equal(t, defaultCheckInterval, cfg.Interval)
	assert.Equal(t, defaultOperationTimeout, cfg.OperationTimeout)
	assert.Zero(t, cfg.RetentionMonths, "zero retention means keep everything and must survive defaulting")
}
