package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/easyp-tech/service/internal/database"
)

const (
	partitionTable      = "audit_log"
	partitionNameFormat = "audit_log_y%04dm%02d"

	// partitionAdvisoryLock serialises maintenance across replicas. Transaction
	// scoped, so it is released on commit or rollback without leaking into a
	// pooled connection.
	partitionAdvisoryLock int64 = 5618204931100741

	labelResult   = "result"
	resultSuccess = "success"
	resultError   = "error"
	resultSkipped = "skipped"
)

// Defaults applied when the config carries zero values. A zero Interval would
// panic time.NewTicker, and the YAML config path bypasses envconfig defaults,
// so these are load-bearing rather than cosmetic.
const (
	defaultRetentionMonths  = 12
	defaultPreCreateMonths  = 3
	defaultCheckInterval    = 6 * time.Hour
	defaultOperationTimeout = 30 * time.Second

	// dropLockTimeout bounds how long a DROP waits for its ACCESS EXCLUSIVE
	// lock. Dropping an expired partition is never urgent, so it is better to
	// give up and retry next tick than to stall audit inserts.
	dropLockTimeout      = "5s"
	dropStatementTimeout = "30s"
)

// ErrRetentionWouldEmptyTable is returned when retention would drop every
// partition, which indicates a clock or configuration fault rather than a
// legitimate expiry.
var ErrRetentionWouldEmptyTable = errors.New("retention would drop every audit partition")

// partitionNameRe matches only generated monthly partitions. audit_log_default
// deliberately fails it, so retention can never drop the tripwire partition.
var partitionNameRe = regexp.MustCompile(`^audit_log_y(\d{4})m(\d{2})$`)

// PartitionConfig configures the audit_log partition lifecycle.
type PartitionConfig struct {
	// RetentionMonths is how many months of history to keep. 0 disables dropping.
	RetentionMonths int
	// PreCreateMonths is how many months ahead of the current one to pre-create.
	PreCreateMonths int
	// Interval is the period between maintenance passes.
	Interval time.Duration
	// OperationTimeout bounds a single maintenance pass.
	OperationTimeout time.Duration
}

func (c *PartitionConfig) setDefault() {
	if c.RetentionMonths < 0 {
		c.RetentionMonths = defaultRetentionMonths
	}

	if c.PreCreateMonths <= 0 {
		c.PreCreateMonths = defaultPreCreateMonths
	}

	if c.Interval <= 0 {
		c.Interval = defaultCheckInterval
	}

	if c.OperationTimeout <= 0 {
		c.OperationTimeout = defaultOperationTimeout
	}
}

// Maintainer keeps monthly audit_log partitions pre-created and drops expired ones.
type Maintainer struct {
	db     *database.SQL
	cfg    PartitionConfig
	logger *slog.Logger

	runs       *prometheus.CounterVec
	created    prometheus.Counter
	dropped    prometheus.Counter
	lastOK     prometheus.Gauge
	partitions prometheus.Gauge
}

// NewMaintainer creates a Maintainer and registers its metrics.
// Pass a nil registry to skip registration.
func NewMaintainer(
	db *database.SQL, cfg PartitionConfig, logger *slog.Logger,
	reg *prometheus.Registry, namespace string,
) *Maintainer {
	cfg.setDefault()

	runs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "audit_partition_maintenance_runs_total",
		Help:      "Total number of audit partition maintenance passes, by result.",
	}, []string{labelResult})

	created := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "audit_partitions_created_total",
		Help:      "Total number of audit_log partitions created.",
	})

	dropped := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "audit_partitions_dropped_total",
		Help:      "Total number of expired audit_log partitions dropped.",
	})

	lastOK := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "audit_partition_maintenance_last_success_seconds",
		Help:      "Unix timestamp of the last successful audit partition maintenance pass.",
	})

	partitions := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "audit_partitions_current",
		Help:      "Current number of monthly audit_log partitions.",
	})

	for _, result := range []string{resultSuccess, resultError, resultSkipped} {
		runs.WithLabelValues(result)
	}

	if reg != nil {
		reg.MustRegister(runs, created, dropped, lastOK, partitions)
	}

	return &Maintainer{
		db:         db,
		cfg:        cfg,
		logger:     logger,
		runs:       runs,
		created:    created,
		dropped:    dropped,
		lastOK:     lastOK,
		partitions: partitions,
	}
}

// Run performs a pass immediately, then on every Interval tick until the
// context is cancelled.
//
// It always returns nil: serve.Start runs services in an errgroup where the
// first non-nil error tears down the whole process, and a failed partition pass
// must never do that — the DEFAULT partition keeps inserts working meanwhile.
func (m *Maintainer) Run(ctx context.Context) error {
	m.runOnce(ctx)

	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

// runOnce executes one bounded maintenance pass and records its outcome.
func (m *Maintainer) runOnce(ctx context.Context) { //nolint:funcorder // helper of Run above
	ctx, cancel := context.WithTimeout(ctx, m.cfg.OperationTimeout)
	defer cancel()

	err := m.Maintain(ctx)
	if err != nil {
		m.runs.WithLabelValues(resultError).Inc()
		m.logger.Error("audit partition maintenance failed", "error", err)

		return
	}

	m.runs.WithLabelValues(resultSuccess).Inc()
	m.lastOK.SetToCurrentTime()
}

// Maintain performs one maintenance pass: pre-create upcoming partitions, then
// drop the ones that fell out of the retention window.
//
// The two halves run in separate transactions so a wedged month cannot stop
// retention, and a blocked drop cannot stop pre-creation.
func (m *Maintainer) Maintain(ctx context.Context) error {
	now := time.Now().UTC()

	created, err := m.EnsurePartitions(ctx, upcomingMonths(now, m.cfg.PreCreateMonths))
	if err != nil {
		return fmt.Errorf("m.EnsurePartitions: %w", err)
	}

	m.created.Add(float64(created))

	if !m.retentionEnabled() {
		return nil
	}

	dropped, err := m.DropExpiredPartitions(ctx, retentionCutoff(now, m.cfg.RetentionMonths))
	if err != nil {
		return fmt.Errorf("m.DropExpiredPartitions: %w", err)
	}

	m.dropped.Add(float64(len(dropped)))

	if len(dropped) > 0 {
		m.logger.Info("dropped expired audit partitions", "count", len(dropped), "partitions", dropped)
	}

	return nil
}

// EnsurePartitions creates any missing partitions for the given months and
// reports how many were created.
func (m *Maintainer) EnsurePartitions(ctx context.Context, months []time.Time) (int, error) {
	var created int

	// Tx derives its metric label from the calling frame and panics when called
	// from a package-level function, so it must be invoked from a method body.
	err := m.db.Tx(ctx, nil, func(tx *sqlx.Tx) error {
		locked, lockErr := acquireLock(ctx, tx)
		if lockErr != nil {
			return lockErr
		}

		if !locked {
			return nil
		}

		for _, month := range months {
			lo := monthStartUTC(month)
			hi := lo.AddDate(0, 1, 0)

			// Identifiers and range bounds cannot be bind parameters. RFC3339
			// renders an explicit Z offset, so the bound is the same absolute
			// instant regardless of the session TimeZone.
			stmt := fmt.Sprintf(
				"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
				pq.QuoteIdentifier(partitionName(lo)),
				pq.QuoteIdentifier(partitionTable),
				pq.QuoteLiteral(lo.Format(time.RFC3339)),
				pq.QuoteLiteral(hi.Format(time.RFC3339)),
			)

			_, execErr := tx.ExecContext(ctx, stmt)
			if execErr != nil {
				return fmt.Errorf("tx.ExecContext %s: %w", partitionName(lo), execErr)
			}

			created++
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("m.db.Tx: %w", err)
	}

	return created, nil
}

// DropExpiredPartitions drops every monthly partition whose month starts before
// cutoff and returns their names.
func (m *Maintainer) DropExpiredPartitions(ctx context.Context, cutoff time.Time) ([]string, error) {
	var dropped []string

	err := m.db.Tx(ctx, nil, func(tx *sqlx.Tx) error {
		locked, lockErr := acquireLock(ctx, tx)
		if lockErr != nil {
			return lockErr
		}

		if !locked {
			return nil
		}

		// DROP takes ACCESS EXCLUSIVE on the parent, which blocks audit inserts
		// while it waits. Bound the wait so maintenance can never become an
		// outage; a skipped drop is retried on the next tick.
		_, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = "+pq.QuoteLiteral(dropLockTimeout))
		if err != nil {
			return fmt.Errorf("set lock_timeout: %w", err)
		}

		_, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = "+pq.QuoteLiteral(dropStatementTimeout))
		if err != nil {
			return fmt.Errorf("set statement_timeout: %w", err)
		}

		names, err := listPartitions(ctx, tx)
		if err != nil {
			return err
		}

		expired := expiredPartitions(names, cutoff)
		if len(expired) > 0 && len(expired) == len(monthlyPartitions(names)) {
			return ErrRetentionWouldEmptyTable
		}

		for _, name := range expired {
			_, err = tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+pq.QuoteIdentifier(name))
			if err != nil {
				return fmt.Errorf("drop partition %s: %w", name, err)
			}

			dropped = append(dropped, name)
		}

		m.partitions.Set(float64(len(monthlyPartitions(names)) - len(dropped)))

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("m.db.Tx: %w", err)
	}

	return dropped, nil
}

func (m *Maintainer) retentionEnabled() bool { //nolint:funcorder // small predicate used by Maintain above
	return m.cfg.RetentionMonths > 0
}

// acquireLock takes the transaction-scoped maintenance lock, reporting whether
// this instance won it.
func acquireLock(ctx context.Context, tx *sqlx.Tx) (bool, error) {
	var locked bool

	err := tx.GetContext(ctx, &locked, "SELECT pg_try_advisory_xact_lock($1)", partitionAdvisoryLock)
	if err != nil {
		return false, fmt.Errorf("pg_try_advisory_xact_lock: %w", err)
	}

	return locked, nil
}

// listPartitions returns the names of every partition attached to audit_log.
func listPartitions(ctx context.Context, tx *sqlx.Tx) ([]string, error) {
	const query = `SELECT c.relname
FROM pg_class c
JOIN pg_inherits i   ON i.inhrelid = c.oid
JOIN pg_class parent ON parent.oid = i.inhparent
JOIN pg_namespace n  ON n.oid = parent.relnamespace
WHERE parent.relname = $1
  AND n.nspname = current_schema()`

	var names []string

	err := tx.SelectContext(ctx, &names, query, partitionTable)
	if err != nil {
		return nil, fmt.Errorf("tx.SelectContext: %w", err)
	}

	return names, nil
}

// monthStartUTC normalises a time to the first instant of its month in UTC.
// Normalising to day 1 first makes AddDate month arithmetic exact.
func monthStartUTC(t time.Time) time.Time {
	t = t.UTC()

	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// partitionName derives the partition name for a month.
func partitionName(month time.Time) string {
	m := monthStartUTC(month)

	return fmt.Sprintf(partitionNameFormat, m.Year(), int(m.Month()))
}

// parsePartitionMonth is the inverse of partitionName. It reports false for
// anything that is not a generated monthly partition.
func parsePartitionMonth(name string) (time.Time, bool) {
	match := partitionNameRe.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}

	year, err := strconv.Atoi(match[1])
	if err != nil {
		return time.Time{}, false
	}

	month, err := strconv.Atoi(match[2])
	if err != nil || month < 1 || month > 12 {
		return time.Time{}, false
	}

	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), true
}

// upcomingMonths lists the current month plus the next ahead months.
func upcomingMonths(now time.Time, ahead int) []time.Time {
	start := monthStartUTC(now)
	months := make([]time.Time, 0, ahead+1)

	for i := 0; i <= ahead; i++ {
		months = append(months, start.AddDate(0, i, 0))
	}

	return months
}

// retentionCutoff returns the first month that must be kept.
func retentionCutoff(now time.Time, months int) time.Time {
	return monthStartUTC(now).AddDate(0, -months, 0)
}

// monthlyPartitions keeps only generated monthly partitions, excluding the
// default partition and any hand-made ones.
func monthlyPartitions(names []string) []string {
	kept := make([]string, 0, len(names))

	for _, name := range names {
		if _, ok := parsePartitionMonth(name); ok {
			kept = append(kept, name)
		}
	}

	return kept
}

// expiredPartitions selects partitions whose month starts before cutoff.
// The current month and anything newer is never expired, whatever the cutoff.
func expiredPartitions(names []string, cutoff time.Time) []string {
	var expired []string

	for _, name := range names {
		month, ok := parsePartitionMonth(name)
		if !ok {
			continue
		}

		if month.Before(cutoff) {
			expired = append(expired, name)
		}
	}

	return expired
}
