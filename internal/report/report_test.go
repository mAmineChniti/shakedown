package report

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mAmineChniti/shakedown/internal/checks"
	"github.com/mAmineChniti/shakedown/internal/runner"
)

func sampleResults() []runner.MigrationResult {
	return []runner.MigrationResult{
		{
			File:         "migrations/001_add_age.sql",
			Duration:     123 * time.Millisecond,
			RowsAffected: 50000,
			Locks: []runner.LockObservation{
				{Relation: "users", Mode: "AccessExclusiveLock", Granted: true},
			},
			Findings: []checks.Finding{
				{
					Check: "data-safety", Severity: checks.Warning,
					Message:    "CREATE INDEX idx_users_age takes a SHARE lock that blocks writes",
					Evidence:   "no CONCURRENTLY in statement",
					Suggestion: "use CREATE INDEX CONCURRENTLY",
				},
			},
			DownFile: "migrations/001_add_age.down.sql",
			Rollback: []checks.Finding{
				{Check: "rollback", Severity: checks.Info, Message: "down migration restores the pre-migration schema", Evidence: "normalized schema diff is empty"},
			},
		},
		{
			File:     "migrations/002_drop.sql",
			Duration: 4 * time.Millisecond,
			Err:      "ERROR: column \"missing\" does not exist (SQLSTATE 42703)",
			Findings: []checks.Finding{
				{
					Check: "data-safety", Severity: checks.Critical,
					Message:  "dropping column name discards 50000 non-null values",
					Evidence: "non-null count = 50000",
				},
			},
		},
	}
}

func TestGoldenFormats(t *testing.T) {
	for _, format := range []string{"text", "json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, sampleResults(), format); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden."+format)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create)", err)
			}
			if buf.String() != string(want) {
				t.Fatalf("output mismatch for %s format\n--- got ---\n%s\n--- want ---\n%s", format, buf.String(), want)
			}
		})
	}
}

func TestMaxSeverity(t *testing.T) {
	if got := MaxSeverity(sampleResults()); got != checks.Critical {
		t.Fatalf("got %s want critical", got)
	}
	clean := []runner.MigrationResult{{File: "x.sql", Duration: time.Millisecond}}
	if got := MaxSeverity(clean); got != checks.Info {
		t.Fatalf("got %s want info", got)
	}
}
