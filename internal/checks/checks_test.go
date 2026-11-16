package checks

import (
	"reflect"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"simple", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"semicolon in string", "SELECT ';'; SELECT 2;", []string{"SELECT ';'", "SELECT 2"}},
		{"dollar quoted", "DO $$ BEGIN PERFORM 1; END $$; SELECT 1;", []string{"DO $$ BEGIN PERFORM 1; END $$", "SELECT 1"}},
		{"line comment", "-- comment ;\nSELECT 1;", []string{"-- comment ;\nSELECT 1"}},
		{"block comment", "/* ; */ SELECT 1;", []string{"/* ; */ SELECT 1"}},
		{"escaped quote", "SELECT 'it''s'; SELECT 2;", []string{"SELECT 'it''s'", "SELECT 2"}},
		{"no trailing semi", "SELECT 1", []string{"SELECT 1"}},
		{"empty", ";;;", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.sql)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSplitActions(t *testing.T) {
	got := splitActions("ADD COLUMN a int, DROP COLUMN b, ALTER COLUMN c TYPE text")
	want := []string{"ADD COLUMN a int", "DROP COLUMN b", "ALTER COLUMN c TYPE text"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	// comma inside parens must not split
	got = splitActions("ADD CONSTRAINT c CHECK (a IN (1,2)), DROP COLUMN b")
	want = []string{"ADD CONSTRAINT c CHECK (a IN (1,2))", "DROP COLUMN b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestIndexColumns(t *testing.T) {
	if got := indexColumns("CREATE INDEX i ON t (a, b) WHERE x"); got != "a, b" {
		t.Fatalf("got %q", got)
	}
	if got := indexColumns("CREATE INDEX i ON t USING btree (lower(email))"); got != "lower(email)" {
		t.Fatalf("got %q", got)
	}
}

func TestFirstParen(t *testing.T) {
	if got := firstParen("ADD CONSTRAINT c UNIQUE (a, b) NOT VALID"); got != "a, b" {
		t.Fatalf("got %q", got)
	}
}

func TestColName(t *testing.T) {
	if got := colName(`"my col" SET DEFAULT 1`); got != `"my col"` {
		t.Fatalf("got %q", got)
	}
	if got := colName("age TYPE int"); got != "age" {
		t.Fatalf("got %q", got)
	}
}

func TestSeverityRank(t *testing.T) {
	if Critical.Rank() <= Warning.Rank() || Warning.Rank() <= Info.Rank() {
		t.Fatal("severity ranks not ordered")
	}
}
