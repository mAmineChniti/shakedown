package schema

import (
	"strings"
	"testing"
)

func tbl(cols map[string]string, idx map[string]string) TableInfo {
	return TableInfo{Columns: cols, Indexes: idx}
}

func TestDiffIdentical(t *testing.T) {
	before := map[string]TableInfo{
		"public.users": tbl(map[string]string{"id": "integer", "name": "text"}, map[string]string{"users_pkey": "CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)"}),
	}
	if d := Diff(before, before); len(d) != 0 {
		t.Fatalf("expected no diffs, got %v", d)
	}
}

func TestDiffColumnTypeChange(t *testing.T) {
	before := map[string]TableInfo{"public.t": tbl(map[string]string{"c": "integer"}, nil)}
	after := map[string]TableInfo{"public.t": tbl(map[string]string{"c": "bigint"}, nil)}
	d := Diff(before, after)
	if len(d) != 1 || !strings.Contains(d[0], "type changed") {
		t.Fatalf("got %v", d)
	}
}

func TestDiffMissingAndExtra(t *testing.T) {
	before := map[string]TableInfo{"public.a": tbl(nil, nil)}
	after := map[string]TableInfo{"public.b": tbl(nil, nil)}
	d := Diff(before, after)
	if len(d) != 2 {
		t.Fatalf("got %v", d)
	}
}

func TestDiffIndexChange(t *testing.T) {
	before := map[string]TableInfo{"public.t": tbl(nil, map[string]string{"i": "CREATE INDEX i ON t (a)"})}
	after := map[string]TableInfo{"public.t": tbl(nil, map[string]string{})}
	d := Diff(before, after)
	if len(d) != 1 || !strings.Contains(d[0], "index i missing") {
		t.Fatalf("got %v", d)
	}
}

func TestDiffRowCountIgnored(t *testing.T) {
	// Row counts are data, not schema — a rollback can't undelete rows and
	// the diff must not flag them.
	before := map[string]TableInfo{"public.t": {Columns: map[string]string{}, RowCount: 5}}
	after := map[string]TableInfo{"public.t": {Columns: map[string]string{}, RowCount: 3}}
	if d := Diff(before, after); len(d) != 0 {
		t.Fatalf("row counts must not produce schema diffs, got %v", d)
	}
}
