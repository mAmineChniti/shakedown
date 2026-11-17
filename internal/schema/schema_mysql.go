package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// snapshotMySQL reads information_schema for all base tables in all
// non-system schemas.
func snapshotMySQL(ctx context.Context, db *sql.DB) (map[string]TableInfo, error) {
	out := map[string]TableInfo{}

	type key struct{ schema, name string }
	infos := map[key]*TableInfo{}

	rows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name
		FROM information_schema.tables
		WHERE table_type = 'BASE TABLE'
		  AND table_schema NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.schema, &k.name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		infos[k] = &TableInfo{
			Name:        k.name,
			Schema:      k.schema,
			Columns:     map[string]string{},
			Indexes:     map[string]string{},
			Constraints: map[string]string{},
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return out, nil
	}

	colRows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name, column_name,
		       CASE
		         WHEN data_type IN ('varchar','char','varbinary','binary')
		           THEN CONCAT(data_type, '(', COALESCE(character_maximum_length, 0), ')')
		         WHEN data_type IN ('decimal','numeric')
		           THEN CONCAT(data_type, '(', COALESCE(numeric_precision, 0), ',', COALESCE(numeric_scale, 0), ')')
		         WHEN data_type IN ('enum','set') THEN column_type
		         ELSE data_type
		       END AS formatted
		FROM information_schema.columns
		WHERE table_schema NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = colRows.Close() }()
	for colRows.Next() {
		var s, t, col, typ string
		if err := colRows.Scan(&s, &t, &col, &typ); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			ti.Columns[col] = typ
		}
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}

	idxRows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name, index_name,
		       GROUP_CONCAT(column_name ORDER BY seq_in_index),
		       MAX(non_unique)
		FROM information_schema.statistics
		WHERE table_schema NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')
		GROUP BY table_schema, table_name, index_name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = idxRows.Close() }()
	for idxRows.Next() {
		var s, t, name, cols string
		var nonUnique int
		if err := idxRows.Scan(&s, &t, &name, &cols, &nonUnique); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			kind := "UNIQUE INDEX"
			if nonUnique == 1 {
				kind = "INDEX"
			}
			ti.Indexes[name] = fmt.Sprintf("%s (%s) ON %s.%s (%s)", kind, name, s, t, cols)
		}
	}
	if err := idxRows.Err(); err != nil {
		return nil, err
	}

	conRows, err := db.QueryContext(ctx, `
		SELECT table_schema, table_name, constraint_name, constraint_type
		FROM information_schema.table_constraints
		WHERE table_schema NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')
		  AND constraint_type IN ('CHECK', 'FOREIGN KEY', 'UNIQUE')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conRows.Close() }()
	for conRows.Next() {
		var s, t, name, ctype string
		if err := conRows.Scan(&s, &t, &name, &ctype); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			ti.Constraints[name] = ctype
		}
	}
	if err := conRows.Err(); err != nil {
		return nil, err
	}

	keys := make([]key, 0, len(infos))
	for k := range infos {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].schema != keys[j].schema {
			return keys[i].schema < keys[j].schema
		}
		return keys[i].name < keys[j].name
	})
	for _, k := range keys {
		ti := infos[k]
		var n int64
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s.%s", qi(k.schema), qi(k.name))).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s.%s: %w", k.schema, k.name, err)
		}
		ti.RowCount = n
		out[k.schema+"."+k.name] = *ti
	}
	return out, nil
}

func qi(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}
