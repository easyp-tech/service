// Package audit provides an audit log adapter for PostgreSQL.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/easyp-tech/service/internal/core"
	"github.com/easyp-tech/service/internal/database"
)

var _ core.AuditLog = &Store{}

// Store реализует core.AuditLog для PostgreSQL.
type Store struct {
	db     *database.SQL
	logger *slog.Logger
}

// New создаёт новый Store.
func New(db *database.SQL, logger *slog.Logger) *Store {
	return &Store{
		db:     db,
		logger: logger,
	}
}

// columnsPerEntry is the number of columns written per audit row.
const columnsPerEntry = 10

const insertPrefix = `INSERT INTO audit_log (
	id, operation_type, plugin_name, caller_address, status,
	error_code, error_message, duration_ms, metadata, created_at
) VALUES `

// SaveBatch реализует core.AuditLog: пишет группу записей одним стейтментом.
func (s *Store) SaveBatch(ctx context.Context, entries []core.AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}

	query, args := s.buildInsert(entries)

	// NoTxContext derives its metric label from the calling function, so it has
	// to be invoked straight from the exported DAL method.
	err := s.db.NoTxContext(ctx, func(db *sqlx.DB) error {
		_, execErr := db.ExecContext(ctx, query, args...)
		if execErr != nil {
			return fmt.Errorf("db.ExecContext: %w", execErr)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("s.db.NoTxContext: %w", err)
	}

	return nil
}

// buildInsert renders a multi-row INSERT with positional placeholders and the
// matching argument list.
func (s *Store) buildInsert(entries []core.AuditEntry) (string, []any) {
	var query strings.Builder

	query.WriteString(insertPrefix)

	args := make([]any, 0, len(entries)*columnsPerEntry)

	for row, entry := range entries {
		if row > 0 {
			query.WriteString(", ")
		}

		query.WriteString("(")

		for col := range columnsPerEntry {
			if col > 0 {
				query.WriteString(", ")
			}

			query.WriteString("$")
			query.WriteString(strconv.Itoa(row*columnsPerEntry + col + 1))
		}

		query.WriteString(")")

		metadata, err := json.Marshal(entry.Metadata)
		if err != nil {
			s.logger.Error("audit metadata marshal failed", "error", err, "entry_id", entry.ID)

			metadata = []byte("{}")
		}

		args = append(args,
			entry.ID,
			entry.OperationType,
			entry.PluginName,
			entry.CallerAddress,
			entry.Status,
			entry.ErrorCode,
			entry.ErrorMessage,
			entry.DurationMs,
			metadata,
			entry.CreatedAt,
		)
	}

	return query.String(), args
}
