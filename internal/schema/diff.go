package schema

import (
	"fmt"
	"sort"
	"strings"
)

// Diff compares two snapshots and returns human-readable differences.
// It covers tables, columns/types, indexes, and constraints — the schema
// surface a
// rollback is expected to restore. Row counts are intentionally excluded:
// a rollback cannot undelete data, and data differences are reported by
// the row-count check instead.
func Diff(before, after map[string]TableInfo) []string {
	var diffs []string

	for key := range before {
		if _, ok := after[key]; !ok {
			diffs = append(diffs, fmt.Sprintf("table %s missing after rollback", key))
		}
	}
	for key, a := range after {
		b, ok := before[key]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("unexpected table %s present after rollback", key))
			continue
		}
		diffs = append(diffs, diffTable(key, b, a)...)
	}
	sort.Strings(diffs)
	return diffs
}

func diffTable(key string, b, a TableInfo) []string {
	var out []string

	for col, bt := range b.Columns {
		at, ok := a.Columns[col]
		if !ok {
			out = append(out, fmt.Sprintf("%s.%s column missing after rollback", key, col))
		} else if at != bt {
			out = append(out, fmt.Sprintf("%s.%s type changed: %s -> %s", key, col, bt, at))
		}
	}
	for col := range a.Columns {
		if _, ok := b.Columns[col]; !ok {
			out = append(out, fmt.Sprintf("%s.%s column added by rollback", key, col))
		}
	}

	for name, bdef := range b.Indexes {
		adef, ok := a.Indexes[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s index %s missing after rollback", key, name))
		} else if normalizeDef(adef) != normalizeDef(bdef) {
			out = append(out, fmt.Sprintf("%s index %s definition differs after rollback", key, name))
		}
	}
	for name := range a.Indexes {
		if _, ok := b.Indexes[name]; !ok {
			out = append(out, fmt.Sprintf("%s index %s added by rollback", key, name))
		}
	}

	for name, bdef := range b.Constraints {
		adef, ok := a.Constraints[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s constraint %s missing after rollback", key, name))
		} else if normalizeDef(adef) != normalizeDef(bdef) {
			out = append(out, fmt.Sprintf("%s constraint %s definition differs after rollback", key, name))
		}
	}
	for name := range a.Constraints {
		if _, ok := b.Constraints[name]; !ok {
			out = append(out, fmt.Sprintf("%s constraint %s added by rollback", key, name))
		}
	}
	return out
}

// normalizeDef strips schema qualification noise so index definitions
// compare stably across snapshots.
func normalizeDef(def string) string {
	return strings.Join(strings.Fields(def), " ")
}
