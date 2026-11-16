// Package schema provides catalog introspection and normalized schema
// snapshots used for rewrite detection, data-safety checks, and rollback
// diffs. Catalog queries are per-dialect; we never shell out to a dump
// tool for diffs.
package schema

import (
	"context"
	"database/sql"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

// TableInfo captures per-table catalog state relevant to verification.
type TableInfo struct {
	Name        string            `json:"name"`
	Schema      string            `json:"schema"`
	RelFileNode int64             `json:"relfilenode"`
	Columns     map[string]string `json:"columns"`     // column -> formatted type
	Indexes     map[string]string `json:"indexes"`     // index name -> definition
	Constraints map[string]string `json:"constraints"` // constraint name -> definition
	RowCount    int64             `json:"row_count"`
}

// Snapshot returns catalog state for all user tables, dispatched by
// dialect.
func Snapshot(ctx context.Context, db *sql.DB, dialect engine.Dialect) (map[string]TableInfo, error) {
	switch dialect {
	case engine.MySQL:
		return snapshotMySQL(ctx, db)
	case engine.SQLite:
		return snapshotSQLite(ctx, db)
	case engine.MSSQL:
		return snapshotMSSQL(ctx, db)
	case engine.Oracle:
		return snapshotOracle(ctx, db)
	default:
		return snapshotPostgres(ctx, db)
	}
}

// RelFileNodes returns table -> relfilenode for rewrite detection
// (Postgres only; other dialects leave it zero).
func RelFileNodes(tables map[string]TableInfo) map[string]int64 {
	m := make(map[string]int64, len(tables))
	for k, t := range tables {
		m[k] = t.RelFileNode
	}
	return m
}
