package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// snapshotPostgres reads pg_catalog for all ordinary tables.
func snapshotPostgres(ctx context.Context, db *sql.DB) (map[string]TableInfo, error) {
	out := map[string]TableInfo{}

	rows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname, c.relfilenode::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		  AND n.nspname NOT LIKE 'pg_temp%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct{ schema, name string }
	infos := map[key]*TableInfo{}
	for rows.Next() {
		var k key
		var relfilenode int64
		if err := rows.Scan(&k.schema, &k.name, &relfilenode); err != nil {
			return nil, err
		}
		infos[k] = &TableInfo{
			Name:        k.name,
			Schema:      k.schema,
			RelFileNode: relfilenode,
			Columns:     map[string]string{},
			Indexes:     map[string]string{},
			Constraints: map[string]string{},
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return out, nil
	}

	colRows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		  AND n.nspname NOT LIKE 'pg_temp%'`)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()
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
		SELECT schemaname, tablename, indexname, indexdef
		FROM pg_indexes
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
		  AND schemaname NOT LIKE 'pg_toast%'
		  AND schemaname NOT LIKE 'pg_temp%'`)
	if err != nil {
		return nil, err
	}
	defer idxRows.Close()
	for idxRows.Next() {
		var s, t, name, def string
		if err := idxRows.Scan(&s, &t, &name, &def); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			ti.Indexes[name] = def
		}
	}
	if err := idxRows.Err(); err != nil {
		return nil, err
	}

	conRows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname, con.conname, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg_toast%'
		  AND n.nspname NOT LIKE 'pg_temp%'`)
	if err != nil {
		return nil, err
	}
	defer conRows.Close()
	for conRows.Next() {
		var s, t, name, def string
		if err := conRows.Scan(&s, &t, &name, &def); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			ti.Constraints[name] = def
		}
	}
	if err := conRows.Err(); err != nil {
		return nil, err
	}

	for k, ti := range infos {
		var n int64
		q := fmt.Sprintf("SELECT count(*) FROM %s.%s", quoteIdent(k.schema), quoteIdent(k.name))
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s.%s: %w", k.schema, k.name, err)
		}
		ti.RowCount = n
		out[k.schema+"."+k.name] = *ti
	}
	return out, nil
}

func quoteIdent(s string) string { return `"` + s + `"` }
