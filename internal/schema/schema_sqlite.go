package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// snapshotSQLite reads sqlite_master + PRAGMA table_info/index_list.
// SQLite has no separate constraint catalog — CHECK/FK/UNIQUE live inside
// the CREATE TABLE sql text, so Constraints stays empty (documented).
func snapshotSQLite(ctx context.Context, db *sql.DB) (map[string]TableInfo, error) {
	out := map[string]TableInfo{}

	rows, err := db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	type key struct{ name string }
	infos := map[key]*TableInfo{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		infos[key{name}] = &TableInfo{
			Name:        name,
			Schema:      "main",
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

	for k := range infos {
		// PRAGMA can't take bound params; name comes from sqlite_master.
		colRows, err := db.QueryContext(ctx,
			fmt.Sprintf("PRAGMA table_info(%s)", qi(k.name)))
		if err != nil {
			return nil, err
		}
		for colRows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := colRows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				colRows.Close()
				return nil, err
			}
			infos[k].Columns[name] = typ
		}
		colRows.Close()
		if err := colRows.Err(); err != nil {
			return nil, err
		}

		idxRows, err := db.QueryContext(ctx,
			fmt.Sprintf("PRAGMA index_list(%s)", qi(k.name)))
		if err != nil {
			return nil, err
		}
		type idx struct{ name, origin string }
		var idxs []idx
		for idxRows.Next() {
			var seq int
			var name, unique, origin, partial string
			if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				idxRows.Close()
				return nil, err
			}
			idxs = append(idxs, idx{name: name, origin: origin})
		}
		idxRows.Close()
		for _, ix := range idxs {
			infoRows, err := db.QueryContext(ctx,
				fmt.Sprintf("PRAGMA index_info(%s)", qi(ix.name)))
			if err != nil {
				return nil, err
			}
			var cols []string
			for infoRows.Next() {
				var seqno, cid int
				var colName sql.NullString
				if err := infoRows.Scan(&seqno, &cid, &colName); err != nil {
					infoRows.Close()
					return nil, err
				}
				cols = append(cols, colName.String)
			}
			infoRows.Close()
			kind := "INDEX"
			if ix.origin == "pk" {
				kind = "PRIMARY KEY"
			}
			infos[k].Indexes[ix.name] = fmt.Sprintf("%s (%s) ON %s (%s)", kind, ix.name, k.name, strings.Join(cols, ","))
		}
	}

	keys := make([]key, 0, len(infos))
	for k := range infos {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].name < keys[j].name })
	for _, k := range keys {
		ti := infos[k]
		var n int64
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s", qi(k.name))).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", k.name, err)
		}
		ti.RowCount = n
		out["main."+k.name] = *ti
	}
	return out, nil
}

// qi is shared with schema_mysql.go (same package).
