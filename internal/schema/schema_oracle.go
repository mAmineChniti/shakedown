package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// snapshotOracle reads ALL_TABLES/ALL_TAB_COLUMNS for the connected user.
// Indexes/constraints are not diffed for Oracle v1 (documented).
func snapshotOracle(ctx context.Context, db *sql.DB) (map[string]TableInfo, error) {
	out := map[string]TableInfo{}

	rows, err := db.QueryContext(ctx, `
		SELECT table_name FROM user_tables
		WHERE table_name NOT LIKE '%$%'`) // exclude Oracle internal MVIEW$_* objects
	if err != nil {
		return nil, err
	}
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	infos := map[string]*TableInfo{}
	for _, n := range names {
		infos[n] = &TableInfo{
			Name: n, Schema: "", Columns: map[string]string{},
			Indexes: map[string]string{}, Constraints: map[string]string{},
		}
	}

	colRows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type ||
		       CASE WHEN data_type IN ('VARCHAR2','CHAR','NVARCHAR2','NCHAR','RAW')
		             THEN '(' || data_length || ')'
		            WHEN data_type IN ('NUMBER') AND data_precision IS NOT NULL
		             THEN '(' || data_precision || ',' || NVL(data_scale,0) || ')'
		            ELSE '' END
		FROM user_tab_columns`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = colRows.Close() }()
	for colRows.Next() {
		var t, c, ty string
		if err := colRows.Scan(&t, &c, &ty); err != nil {
			return nil, err
		}
		if ti, ok := infos[t]; ok {
			ti.Columns[c] = ty
		}
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	sort.Strings(names)
	for _, n := range names {
		ti := infos[n]
		var cnt int64
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s", qora(n))).Scan(&cnt); err != nil {
			return nil, fmt.Errorf("count %s: %w", n, err)
		}
		ti.RowCount = cnt
		out[n] = *ti
	}
	return out, nil
}

func qora(s string) string { return `"` + s + `"` }
