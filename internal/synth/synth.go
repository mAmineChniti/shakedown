// Package synth generates synthetic rows for baseline tables so data-safety
// checks run against realistic volume. The stats file is line-based:
//
//	# comments allowed
//	table_name row_count
//	orders 500000
//
// Column types are introspected from the baseline schema; values are
// deterministic functions of the row number.
package synth

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

// LoadStats parses a stats file into table -> row count.
func LoadStats(path string) (map[string]int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for ln, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: expected 'table rows', got %q", path, ln+1, line)
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s:%d: invalid row count %q", path, ln+1, fields[1])
		}
		out[fields[0]] = n
	}
	return out, nil
}

// Generate inserts synthetic rows for each table in stats. Tables are
// processed in name order; stats authors must respect foreign keys
// (parent tables need enough rows for child references).
func Generate(ctx context.Context, db *sql.DB, dialect engine.Dialect, stats map[string]int64) error {
	if dialect == engine.Oracle {
		return fmt.Errorf("synthetic data generation is not yet supported for oracle")
	}
	names := make([]string, 0, len(stats))
	for t := range stats {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, table := range names {
		if stats[table] == 0 {
			continue
		}
		if err := generateTable(ctx, db, dialect, table, stats[table]); err != nil {
			return fmt.Errorf("synth %s: %w", table, err)
		}
	}
	return nil
}

type column struct {
	name     string
	typ      string
	deflt    string
	identity string
	extra    string
}

func generateTable(ctx context.Context, db *sql.DB, d engine.Dialect, table string, rows int64) error {
	var colQuery string
	switch d {
	case engine.MySQL:
		colQuery = `
			SELECT column_name, data_type, COALESCE(column_default, ''),
			       CASE WHEN extra LIKE '%auto_increment%' THEN 'YES' ELSE '' END,
			       COALESCE(extra, '')
			FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = '` + strings.ReplaceAll(table, "`", "``") + `'
			ORDER BY ordinal_position`
	case engine.SQLite:
		colQuery = `SELECT name, type, COALESCE(dflt_value, ''), '', CAST(pk AS TEXT)
			FROM pragma_table_info('` + strings.ReplaceAll(table, "'", "''") + `')`
	case engine.MSSQL:
		colQuery = `
			SELECT c.name, ty.name, '',
			       CASE WHEN c.is_identity = 1 THEN 'YES' ELSE '' END, ''
			FROM sys.columns c
			JOIN sys.tables t ON t.object_id = c.object_id
			JOIN sys.schemas s ON s.schema_id = t.schema_id
			JOIN sys.types ty ON ty.user_type_id = c.user_type_id
			WHERE t.is_ms_shipped = 0 AND s.name = 'dbo' AND t.name = '` +
			strings.ReplaceAll(table, "'", "''") + `'
			ORDER BY c.column_id`
	default:
		colQuery = `
			SELECT column_name, data_type, COALESCE(column_default, ''), is_identity, ''
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = '` + strings.ReplaceAll(table, "`", "``") + `'
			ORDER BY ordinal_position`
	}
	rowsQ, err := db.QueryContext(ctx, colQuery)
	if err != nil {
		return err
	}
	var cols []column
	for rowsQ.Next() {
		var c column
		if err := rowsQ.Scan(&c.name, &c.typ, &c.deflt, &c.identity, &c.extra); err != nil {
			rowsQ.Close()
			return err
		}
		// Skip sequence-backed and identity columns: they auto-assign,
		// and explicit values would collide with existing rows.
		if strings.Contains(c.deflt, "nextval") || c.identity == "YES" ||
			strings.Contains(strings.ToLower(c.extra), "auto_increment") ||
			strings.Contains(strings.ToLower(c.deflt), "autoincrement") {
			continue
		}
		// SQLite INTEGER PRIMARY KEY is a rowid alias — auto-assigns.
		if d == engine.SQLite && c.extra == "1" {
			continue
		}
		cols = append(cols, c)
	}
	rowsQ.Close()
	if err := rowsQ.Err(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("table %q not found or has no insertable columns", table)
	}

	qi := func(s string) string { return engine.QuoteIdent(d, s) }
	var exprs, names []string
	for _, c := range cols {
		names = append(names, qi(c.name))
		exprs = append(exprs, valueExpr(d, c.name, c.typ))
	}

	var sqlText string
	switch d {
	case engine.MySQL:
		sqlText = fmt.Sprintf(
			"INSERT INTO %s (%s) SELECT %s FROM (SELECT @row := @row + 1 AS g FROM information_schema.tables, (SELECT @row := 0) r LIMIT %d) nums",
			qi(table), strings.Join(names, ", "), strings.Join(exprs, ", "), rows)
	case engine.SQLite:
		sqlText = fmt.Sprintf(
			"WITH RECURSIVE seq(g) AS (SELECT 1 UNION ALL SELECT g+1 FROM seq WHERE g < %d) INSERT INTO %s (%s) SELECT %s FROM seq",
			rows+1, qi(table), strings.Join(names, ", "), strings.Join(exprs, ", "))
	case engine.MSSQL:
		sqlText = fmt.Sprintf(
			";WITH seq AS (SELECT 1 g UNION ALL SELECT g+1 FROM seq WHERE g <= %d) INSERT INTO %s (%s) SELECT %s FROM seq OPTION (MAXRECURSION 0)",
			rows, qi(table), strings.Join(names, ", "), strings.Join(exprs, ", "))
	default:
		sqlText = fmt.Sprintf(
			"INSERT INTO %s (%s) SELECT %s FROM generate_series(1, %d) g",
			qi(table), strings.Join(names, ", "), strings.Join(exprs, ", "), rows)
	}
	_, err = db.ExecContext(ctx, sqlText)
	return err
}

// valueExpr maps a column to a deterministic expression over g.
func valueExpr(d engine.Dialect, name, typ string) string {
	l := strings.ToLower(typ)
	switch {
	case strings.Contains(l, "int") || strings.Contains(l, "serial") ||
		strings.Contains(l, "numeric") || strings.Contains(l, "decimal") ||
		l == "real" || strings.Contains(l, "double") || strings.Contains(l, "float"):
		return "g"
	case strings.Contains(l, "bool") || strings.Contains(l, "bit"):
		return "(g % 2 = 0)"
	case strings.Contains(l, "timestamp") || strings.Contains(l, "datetime") ||
		strings.Contains(l, "date"):
		if d == engine.SQLite {
			return "datetime('now')"
		}
		if d == engine.MSSQL {
			return "GETDATE()"
		}
		return "NOW()"
	case strings.Contains(l, "json"):
		if d == engine.SQLite {
			return "json_object('id', g, 'k', 'v')"
		}
		if d == engine.MSSQL {
			return `'{"id":' + CAST(g AS NVARCHAR) + ',"k":"v"}'`
		}
		if d == engine.MySQL {
			return "JSON_OBJECT('id', g, 'k', 'v')"
		}
		return "jsonb_build_object('id', g, 'k', 'v')"
	case strings.Contains(l, "uuid"):
		if d == engine.SQLite {
			return "lower(hex(randomblob(16)))"
		}
		if d == engine.MSSQL {
			return "NEWID()"
		}
		return "UUID()"
	default:
		lit := strings.ReplaceAll(name, "'", "''")
		if d == engine.MySQL {
			return fmt.Sprintf("CONCAT('synth-', g, '-', '%s')", lit)
		}
		if d == engine.MSSQL {
			return fmt.Sprintf("CONCAT('synth-', CAST(g AS NVARCHAR), '-', '%s')", lit)
		}
		return fmt.Sprintf("('synth-' || g || '-' || '%s')", lit)
	}
}
