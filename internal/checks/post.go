package checks

import (
	"context"
	"fmt"

	"github.com/mAmineChniti/shakedown/internal/schema"
)

// RewriteCheck detects tables whose relfilenode changed during the
// migration — Postgres's definitive signal that the table was rewritten.
// Postgres-only; other dialects report rewrite risk via the data-safety
// pre-check instead.
type RewriteCheck struct{}

func (RewriteCheck) Name() string { return "table-rewrite" }

func (RewriteCheck) Analyze(_ context.Context, _ Session, before, after map[string]schema.TableInfo) []Finding {
	var out []Finding
	for key, b := range before {
		a, ok := after[key]
		if !ok {
			continue // table dropped; handled elsewhere
		}
		if a.RelFileNode != b.RelFileNode {
			out = append(out, Finding{
				Check:    "table-rewrite",
				Severity: Warning,
				Message:  fmt.Sprintf("table %s was rewritten (relfilenode %d -> %d)", key, b.RelFileNode, a.RelFileNode),
				Evidence: fmt.Sprintf("relfilenode changed: %d -> %d", b.RelFileNode, a.RelFileNode),
				Suggestion: "rewrites hold ACCESS EXCLUSIVE locks for the full duration; " +
					"on large tables prefer expand/contract migrations or pg_repack",
			})
		}
	}
	return out
}

// RowCountCheck reports tables whose row counts changed, so reviewers can
// see the data effects of the migration.
type RowCountCheck struct{}

func (RowCountCheck) Name() string { return "row-counts" }

func (RowCountCheck) Analyze(_ context.Context, _ Session, before, after map[string]schema.TableInfo) []Finding {
	var out []Finding
	for key, b := range before {
		a, ok := after[key]
		if !ok {
			continue
		}
		if a.RowCount != b.RowCount {
			out = append(out, Finding{
				Check:    "row-counts",
				Severity: Info,
				Message:  fmt.Sprintf("%s row count changed: %d -> %d", key, b.RowCount, a.RowCount),
				Evidence: fmt.Sprintf("count(*) before=%d after=%d", b.RowCount, a.RowCount),
			})
		}
	}
	// New tables have no "before" — report them too.
	for key, a := range after {
		if _, ok := before[key]; !ok && a.RowCount > 0 {
			out = append(out, Finding{
				Check:    "row-counts",
				Severity: Info,
				Message:  fmt.Sprintf("new table %s created with %d rows", key, a.RowCount),
				Evidence: fmt.Sprintf("count(*) = %d", a.RowCount),
			})
		}
	}
	return out
}
