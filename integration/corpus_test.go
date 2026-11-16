// Package integration exercises shakedown end-to-end against a real
// database container. Requires Docker; skipped under -short.
//
// SHAKEDOWN_ENGINE selects postgres|mysql (default postgres);
// SHAKEDOWN_VERSION selects the image tag (default 16 / 8.4).
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mAmineChniti/shakedown/internal/checks"
	"github.com/mAmineChniti/shakedown/internal/engine"
	"github.com/mAmineChniti/shakedown/internal/engine/mssql"
	"github.com/mAmineChniti/shakedown/internal/engine/mysql"
	"github.com/mAmineChniti/shakedown/internal/engine/postgres"
	"github.com/mAmineChniti/shakedown/internal/engine/sqlite"
	"github.com/mAmineChniti/shakedown/internal/runner"
)

func TestCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires Docker; skipping under -short")
	}
	name := envOr("SHAKEDOWN_ENGINE", "postgres")
	var eng engine.Engine
	var err error
	var defaultVersion string
	switch name {
	case "postgres":
		defaultVersion = "16"
		eng, err = postgres.New(context.Background(), engine.Options{
			Engine: engine.Postgres, Version: envOr("SHAKEDOWN_VERSION", defaultVersion)})
	case "mysql":
		defaultVersion = "8.4"
		eng, err = mysql.New(context.Background(), engine.Options{
			Engine: engine.MySQL, Version: envOr("SHAKEDOWN_VERSION", defaultVersion)})
	case "sqlite":
		eng, err = sqlite.New(context.Background(), engine.Options{Engine: engine.SQLite})
	case "mssql":
		eng, err = mssql.New(context.Background(), engine.Options{
			Engine: engine.MSSQL, Version: envOr("SHAKEDOWN_VERSION", "2022-latest")})
	default:
		t.Fatalf("unknown SHAKEDOWN_ENGINE %q", name)
	}
	if err != nil {
		t.Fatalf("provision %s: %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	defer func() {
		tctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = eng.Teardown(tctx)
	}()

	corpusDir := filepath.Join("..", "testdata", "corpus_"+name)
	baselinePath := filepath.Join("..", "testdata", "baseline_"+name+".sql")

	baseline, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	cases, err := filepath.Glob(filepath.Join(corpusDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(cases)
	if len(cases) < 10 {
		t.Fatalf("corpus %s has %d cases, want >= 10", name, len(cases))
	}

	for _, dir := range cases {
		caseName := filepath.Base(dir)
		t.Run(caseName, func(t *testing.T) {
			resetAndLoad(t, ctx, eng, baseline)

			up := filepath.Join(dir, "up.sql")
			results, runErr := runner.New(eng).Run(ctx, []string{up})
			if len(results) == 0 {
				t.Fatalf("no results (err=%v)", runErr)
			}
			res := results[0]

			expected, err := os.ReadFile(filepath.Join(dir, "expected.txt"))
			if err != nil {
				t.Fatalf("read expected: %v", err)
			}
			verifyExpectations(t, res, runErr != nil || res.Err != "", string(expected))
		})
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func resetAndLoad(t *testing.T, ctx context.Context, eng engine.Engine, baseline []byte) {
	t.Helper()
	conn, err := eng.Connect(ctx)
	if err != nil {
		t.Fatalf("connect for reset: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	switch eng.Dialect() {
	case engine.SQLite:
		// Drop every user table, then reload the baseline.
		tblRows, err := conn.QueryContext(ctx, `
			SELECT name FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		var tables []string
		for tblRows.Next() {
			var n string
			if err := tblRows.Scan(&n); err != nil {
				tblRows.Close()
				t.Fatalf("scan table: %v", err)
			}
			tables = append(tables, `"`+n+`"`)
		}
		tblRows.Close()
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			t.Fatalf("fk off: %v", err)
		}
		for _, tb := range tables {
			if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+tb); err != nil {
				t.Fatalf("drop %s: %v", tb, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			t.Fatalf("fk on: %v", err)
		}
	case engine.MSSQL:
		// Drop FK constraints first (NOCHECK alone doesn't unblock
		// DROP TABLE on the referenced parent), then drop all tables.
		if _, err := conn.ExecContext(ctx, `
			DECLARE @sql NVARCHAR(MAX) = N'';
			SELECT @sql = @sql + N'ALTER TABLE ' + QUOTENAME(SCHEMA_NAME(fk.schema_id)) + N'.' +
				QUOTENAME(OBJECT_NAME(fk.parent_object_id)) + N' DROP CONSTRAINT ' + QUOTENAME(fk.name) + N';'
			FROM sys.foreign_keys fk;
			EXEC sp_executesql @sql;`); err != nil {
			t.Fatalf("drop fks: %v", err)
		}
		tblRows, err := conn.QueryContext(ctx, `
			SELECT t.name FROM sys.tables t
			JOIN sys.schemas s ON s.schema_id = t.schema_id
			WHERE t.is_ms_shipped = 0 AND s.name = 'dbo'`)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		var tables []string
		for tblRows.Next() {
			var n string
			if err := tblRows.Scan(&n); err != nil {
				tblRows.Close()
				t.Fatalf("scan table: %v", err)
			}
			tables = append(tables, "["+n+"]")
		}
		tblRows.Close()
		for _, tb := range tables {
			if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+tb); err != nil {
				t.Fatalf("drop %s: %v", tb, err)
			}
		}
	case engine.MySQL:
		// Drop every user table, then reload the baseline.
		tblRows, err := conn.QueryContext(ctx, `
			SELECT table_name FROM information_schema.tables
			WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'`)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		var tables []string
		for tblRows.Next() {
			var n string
			if err := tblRows.Scan(&n); err != nil {
				tblRows.Close()
				t.Fatalf("scan table: %v", err)
			}
			tables = append(tables, "`"+n+"`")
		}
		tblRows.Close()
		if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
			t.Fatalf("fk checks off: %v", err)
		}
		for _, tb := range tables {
			if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+tb); err != nil {
				t.Fatalf("drop %s: %v", tb, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
			t.Fatalf("fk checks on: %v", err)
		}
	default:
		for _, stmt := range []string{
			"DROP SCHEMA public CASCADE",
			"CREATE SCHEMA public",
		} {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
	}

	if err := execAll(ctx, conn, string(baseline)); err != nil {
		t.Fatalf("load baseline: %v", err)
	}
}

func execAll(ctx context.Context, db *sql.DB, sqlText string) error {
	for _, stmt := range checks.SplitStatements(sqlText) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// verifyExpectations checks each line of expected.txt against the run.
// Lines are case-insensitive substrings that must appear in the combined
// finding text. Special lines:
//   - "clean": no warning/critical findings and no migration error
//   - "error": the migration must have failed
func verifyExpectations(t *testing.T, res runner.MigrationResult, failed bool, expected string) {
	t.Helper()

	var hay strings.Builder
	for _, f := range res.Findings {
		hay.WriteString(strings.ToLower(fmt.Sprintf("%s %s %s %s %s\n", f.Severity, f.Check, f.Message, f.Evidence, f.Suggestion)))
	}
	for _, f := range res.Rollback {
		hay.WriteString(strings.ToLower(fmt.Sprintf("rollback %s %s %s %s\n", f.Severity, f.Check, f.Message, f.Evidence)))
	}
	h := hay.String()

	for _, line := range strings.Split(expected, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line {
		case "clean":
			if strings.Contains(h, "warning") || strings.Contains(h, "critical") {
				t.Errorf("expected clean run, got findings:\n%s", h)
			}
			if res.Err != "" {
				t.Errorf("expected clean run, migration failed: %s", res.Err)
			}
		case "error":
			if !failed {
				t.Errorf("expected migration error, got none")
			}
		default:
			if !strings.Contains(h, strings.ToLower(line)) {
				t.Errorf("expected finding containing %q not found; findings were:\n%s", line, h)
			}
		}
	}
}
