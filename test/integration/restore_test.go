//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/easyp-tech/service/internal/database/goosemigrate"
)

// TestBackupRestoreRoundTrip is the drill docs/BACKUP.md describes and nobody
// had ever run.
//
// Its own closing line — "a backup that has never been restored is a
// hypothesis" — was, until this test, a description of our situation. What it
// checks is exactly what that document promises survives: the registry, the
// audit partitions, and the migration bookkeeping that decides what startup
// does next.
func TestBackupRestoreRoundTrip(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set", dsnEnv)
	}

	requireTool(t, "pg_dump")
	requireTool(t, "psql")

	ctx := context.Background()

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	reset(t, ctx, db)
	require.NoError(t, goosemigrate.Up(ctx, dsn))

	// The registry, with a checksum inside the config document. That is where
	// the sha256 lives, and it is the field a restore most needs to bring back:
	// without it, verification of the plugin binary is silently off.
	const checksum = "8f2b4c1d9e6a3705bd4c8e1f2a9b7c3d5e0f1a2b3c4d5e6f7081920a1b2c3d4e"

	_, err = db.ExecContext(ctx,
		`INSERT INTO plugins (group_name, name, version, config, tags)
		 VALUES ($1, $2, $3, $4, $5)`,
		"grpc", "go", "v1.5.1",
		`{"command":["/plugins/grpc/go/v1.5.1/plugin"],"sha256":"`+checksum+`"}`,
		"{stable,official}",
	)
	require.NoError(t, err)

	// Audit rows in two different months, so the restore has more than one
	// partition to bring back.
	now := time.Now().UTC()
	for _, at := range []time.Time{now, now.AddDate(0, 0, -1)} {
		_, err = db.ExecContext(ctx,
			`INSERT INTO audit_log (operation_type, plugin_name, caller_address, status, duration_ms, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			"generate_code", "grpc/go:v1.5.1", "10.0.0.1", "success", 42, at)
		require.NoError(t, err)
	}

	wantPlugins := count(t, ctx, db, "SELECT count(*) FROM plugins")
	wantAudit := count(t, ctx, db, "SELECT count(*) FROM audit_log")
	wantVersion := count(t, ctx, db, "SELECT max(version_id) FROM goose_db_version")
	wantParts := count(t, ctx, db, partitionCountQuery)

	require.Positive(t, wantParts, "the audit table must be partitioned before this proves anything")

	dump := filepath.Join(t.TempDir(), "backup.sql")
	run(t, "pg_dump", "--dbname="+dsn, "--file="+dump)

	// The disaster.
	reset(t, ctx, db)

	run(t, "psql", "--dbname="+dsn, "--set=ON_ERROR_STOP=1", "--quiet", "--file="+dump)

	assert.Equal(t, wantPlugins, count(t, ctx, db, "SELECT count(*) FROM plugins"),
		"the registry is the one thing nothing else holds")
	assert.Equal(t, wantAudit, count(t, ctx, db, "SELECT count(*) FROM audit_log"))
	assert.Equal(t, wantVersion, count(t, ctx, db, "SELECT max(version_id) FROM goose_db_version"),
		"migration bookkeeping decides what the next startup does")
	assert.Equal(t, wantParts, count(t, ctx, db, partitionCountQuery),
		"partitions must come back as partitions, not as one flat table")

	// The checksum survived inside the config document. Losing it does not fail
	// a restore, it disables verification of the plugin binary — quietly.
	var restored string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT config->>'sha256' FROM plugins WHERE group_name = 'grpc' AND name = 'go'`).Scan(&restored))
	assert.Equal(t, checksum, restored)

	// And BACKUP.md's instruction to let the service migrate holds: there is
	// nothing left to apply.
	require.NoError(t, goosemigrate.Up(ctx, dsn))
	assert.Equal(t, wantVersion, count(t, ctx, db, "SELECT max(version_id) FROM goose_db_version"))
}

// partitionCountQuery counts the partitions attached to audit_log.
const partitionCountQuery = `
SELECT count(*) FROM pg_inherits
JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
WHERE parent.relname = 'audit_log'`

func reset(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	_, err := db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
	require.NoError(t, err)
}

func count(t *testing.T, ctx context.Context, db *sql.DB, query string) int64 {
	t.Helper()

	var n int64
	require.NoError(t, db.QueryRowContext(ctx, query).Scan(&n))

	return n
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()

	out, err := exec.Command(name, args...).CombinedOutput()
	require.NoErrorf(t, err, "%s: %s", name, out)
}

// requireTool skips locally and fails in CI.
//
// A skip is the right answer on a laptop without postgresql-client. It is the
// wrong one on a runner: this is the only test of the restore procedure, and a
// silent skip there would report the drill as passing precisely when it had not
// run — which is the failure mode the drill exists to rule out.
func requireTool(t *testing.T, name string) {
	t.Helper()

	if _, err := exec.LookPath(name); err == nil {
		return
	}

	if os.Getenv("CI") != "" {
		t.Fatalf("%s is not on PATH; the restore drill cannot be skipped in CI", name)
	}

	t.Skipf("%s is not on PATH", name)
}
