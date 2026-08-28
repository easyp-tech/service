//go:build integration

package goosemigrate

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/lib/pq"
)

const dsnEnv = "EASYP_TEST_DSN"

// TestRollbackOntoAnOlderBinary records what actually happens when a database
// is newer than the binary running against it — the case docs/BACKUP.md singles
// out and describes wrongly.
//
// BACKUP.md said the service "refuses to start rather than run against a schema
// it does not understand". It does not. goose only objects to versions that are
// *missing below* the database's maximum: a database at version 2 against a
// binary that knows only version 1 yields an empty apply set and starts
// cleanly.
//
// That matters because it decides what a rollback is. It is not refused, it is
// permitted — and its real limit is elsewhere: the older binary has no
// partition maintainer, so once the months migration 00002 pre-created run out,
// audit rows land in audit_log_default, and a non-empty default partition
// blocks creating the month that would overlap it. A rollback is therefore
// bounded by audit.pre_create_months, not by a startup check.
func TestRollbackOntoAnOlderBinary(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set", dsnEnv)
	}

	ctx := context.Background()

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
	require.NoError(t, err)

	// Forward to the current head, as a running service would.
	require.NoError(t, Up(ctx, dsn))

	var head int64
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT max(version_id) FROM goose_db_version").Scan(&head))
	require.Greater(t, head, int64(1), "this test needs at least two migrations to model a rollback")

	// The migration set an older binary was built with: everything below head.
	older := olderMigrations(t, head)

	provider, err := goose.NewProvider(goose.DialectPostgres, db, older)
	require.NoError(t, err)

	applied, err := provider.Up(ctx)

	// The behaviour, pinned. Not "refuses to start".
	require.NoError(t, err, "an older binary starts against a newer database")
	assert.Empty(t, applied, "and applies nothing")

	// The database is untouched: still at head, so rolling forward again is a
	// no-op rather than a re-run.
	var afterwards int64
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT max(version_id) FROM goose_db_version").Scan(&afterwards))
	assert.Equal(t, head, afterwards)
}

// olderMigrations returns the embedded migrations with everything at or above
// head removed, which is the filesystem a binary from before that migration was
// built with.
func olderMigrations(t *testing.T, head int64) fstest.MapFS {
	t.Helper()

	entries, err := migrationsFS.ReadDir("migrations")
	require.NoError(t, err)

	older := fstest.MapFS{}

	for _, entry := range entries {
		version, parseErr := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, parseErr, entry.Name())

		if version >= head {
			continue
		}

		body, readErr := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, readErr)

		older[entry.Name()] = &fstest.MapFile{Data: body}
	}

	require.NotEmpty(t, older, "the older binary must still know at least one migration")

	return older
}
