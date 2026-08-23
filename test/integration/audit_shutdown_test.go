//go:build integration

package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapter_audit "github.com/easyp-tech/service/internal/adapters/audit"
	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/database"
	"github.com/easyp-tech/service/internal/database/connectors"
)

func auditStore(t *testing.T) (*adapter_audit.Store, *database.SQL) {
	t.Helper()

	dsn := requireDSN(t)

	db, err := database.NewSQL(t.Context(), "postgres", database.SQLConfig{}, &connectors.Raw{Query: dsn})
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	return adapter_audit.New(db, slog.New(slog.DiscardHandler)), db
}

func auditEntry(op string) core.AuditEntry {
	id, _ := uuid.NewV4()

	return core.AuditEntry{
		ID:            id,
		OperationType: op,
		Status:        core.AuditStatusSuccess,
		Metadata:      map[string]any{},
		CreatedAt:     time.Now(),
	}
}

func countByID(t *testing.T, db *database.SQL, id uuid.UUID) int {
	t.Helper()

	var n int
	require.NoError(t, db.UnderlyingDB().QueryRowContext(
		t.Context(), "SELECT count(*) FROM audit_log WHERE id = $1", id,
	).Scan(&n))

	return n
}

// The premise behind detaching the audit writer from the application context,
// checked against a real database rather than assumed.
//
// SaveBatch reaches db.ExecContext, which refuses a cancelled context outright —
// no round trip, no partial write. Since SIGTERM cancels the application context
// while handlers are still running, a writer that flushed on that context lost
// every batch it tried to write for the whole of graceful shutdown.
func TestSaveBatchRejectsCancelledContext(t *testing.T) {
	t.Parallel()

	store, db := auditStore(t)
	entry := auditEntry(core.OperationGenerateCode)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := store.SaveBatch(ctx, []core.AuditEntry{entry})

	require.Error(t, err, "a cancelled context must not reach the database as a successful write")
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, countByID(t, db, entry.ID))
}

// The same worker, wired the way the service wires it, writing through a
// cancelled application context — which is the state every graceful shutdown is
// in from SIGTERM until the queue closes.
func TestWorkerWritesThroughShutdown(t *testing.T) {
	t.Parallel()

	store, db := auditStore(t)

	worker := adapter_audit.NewWorker(store, adapter_audit.Config{
		BufferSize:    100,
		BatchSize:     100,
		FlushInterval: 20 * time.Millisecond,
	}, slog.New(slog.DiscardHandler), nil, "test")

	ctx, cancel := context.WithCancel(t.Context())
	go worker.Run(ctx)

	// SIGTERM: the context dies while handlers are still producing.
	cancel()

	entries := make([]core.AuditEntry, 0, 10)
	for range 10 {
		entry := auditEntry(core.OperationListPlugins)
		entries = append(entries, entry)
		worker.Send(ctx, entry)
	}

	assert.Zero(t, worker.Shutdown(5*time.Second), "no events should be lost draining the queue")

	for _, entry := range entries {
		assert.Equal(t, 1, countByID(t, db, entry.ID),
			"every entry produced during shutdown must reach the database")
	}
}
