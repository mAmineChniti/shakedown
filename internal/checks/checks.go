// Package checks implements the verification rules shakedown applies to
// each migration: pre-flight data-safety probes run before execution, and
// post-run checks (rewrite detection, row-count drift) run after.
package checks

import (
	"context"
	"database/sql"

	"github.com/mAmineChniti/shakedown/internal/engine"
	"github.com/mAmineChniti/shakedown/internal/schema"
)

// Severity ranks findings; exit codes key off the minimum severity.
type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

// Rank orders severities for threshold comparisons.
func (s Severity) Rank() int {
	switch s {
	case Critical:
		return 3
	case Warning:
		return 2
	case Info:
		return 1
	}
	return 0
}

// Finding is a single verified observation. Evidence must come from the
// actual run — never from static guesswork alone.
type Finding struct {
	Check      string   `json:"check"`
	Severity   Severity `json:"severity"`
	Message    string   `json:"message"`
	Evidence   string   `json:"evidence,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// Session carries the live connection and dialect to checks.
type Session struct {
	DB      *sql.DB
	Dialect engine.Dialect
}

// PreCheck runs before a migration executes. It receives the split SQL
// statements and a live connection for count probes.
type PreCheck interface {
	Name() string
	Analyze(ctx context.Context, sess Session, statements []string) []Finding
}

// PostCheck runs after a migration executes, comparing catalog snapshots.
type PostCheck interface {
	Name() string
	Analyze(ctx context.Context, sess Session, before, after map[string]schema.TableInfo) []Finding
}

// ForDialect returns the default check pipeline for an engine.
func ForDialect(d engine.Dialect) (pre []PreCheck, post []PostCheck) {
	pre = []PreCheck{DataSafety{Dialect: d}}
	post = []PostCheck{RowCountCheck{}}
	if d == engine.Postgres {
		// relfilenode gives Postgres a definitive rewrite signal; MySQL
		// has no equivalent, so rewrite risk there is reported by the
		// data-safety pre-check from the ALGORITHM clause instead.
		post = append([]PostCheck{RewriteCheck{}}, post...)
	}
	return pre, post
}
