package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// snapshotMSSQL reads sys catalogs for user tables in all non-system
// schemas.
func snapshotMSSQL(ctx context.Context, db *sql.DB) (map[string]TableInfo, error) {
	out := map[string]TableInfo{}

	rows, err := db.QueryContext(ctx, `
		SELECT s.name, t.name
		FROM sys.tables t
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE t.is_ms_shipped = 0
		  AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA')`)
	if err != nil {
		return nil, err
	}
	type key struct{ schema, name string }
	infos := map[key]*TableInfo{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.schema, &k.name); err != nil {
			rows.Close()
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
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return out, nil
	}

	colRows, err := db.QueryContext(ctx, `
		SELECT s.name, t.name, c.name,
		       ty.name + CASE WHEN c.max_length > 0 AND ty.name IN ('varchar','nvarchar','char','nchar','varbinary','binary')
		                     THEN '(' + CAST(c.max_length AS varchar) + ')'
		                     WHEN c.precision > 0 AND ty.name IN ('decimal','numeric')
		                     THEN '(' + CAST(c.precision AS varchar) + ',' + CAST(c.scale AS varchar) + ')'
		                     ELSE '' END
		FROM sys.columns c
		JOIN sys.tables t ON t.object_id = c.object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		JOIN sys.types ty ON ty.user_type_id = c.user_type_id
		WHERE t.is_ms_shipped = 0
		  AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA')`)
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
		SELECT s.name, t.name, i.name,
		       CASE WHEN i.is_unique = 1 THEN 'UNIQUE INDEX' ELSE 'INDEX' END +
		       ' (' + i.name + ') ON ' + s.name + '.' + t.name
		FROM sys.indexes i
		JOIN sys.tables t ON t.object_id = i.object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE t.is_ms_shipped = 0 AND i.name IS NOT NULL
		  AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA')`)
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
		SELECT s.name, t.name, kc.name, kc.type_desc
		FROM sys.key_constraints kc
		JOIN sys.tables t ON t.object_id = kc.parent_object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE t.is_ms_shipped = 0
		  AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA')`)
	if err != nil {
		return nil, err
	}
	defer conRows.Close()
	for conRows.Next() {
		var s, t, name, typ string
		if err := conRows.Scan(&s, &t, &name, &typ); err != nil {
			return nil, err
		}
		if ti, ok := infos[key{s, t}]; ok {
			ti.Constraints[name] = typ
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
			fmt.Sprintf("SELECT count(*) FROM %s.%s", qbr(k.schema), qbr(k.name))).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s.%s: %w", k.schema, k.name, err)
		}
		ti.RowCount = n
		out[k.schema+"."+k.name] = *ti
	}
	return out, nil
}

func qbr(s string) string { return "[" + s + "]" }

var _ = strings.TrimSpace // keep strings import if unused later
