// Command shakedown verifies database migrations by rehearsing them
// against a throwaway database.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/mAmineChniti/shakedown/internal/checks"
	"github.com/mAmineChniti/shakedown/internal/engine"
	"github.com/mAmineChniti/shakedown/internal/engine/mssql"
	"github.com/mAmineChniti/shakedown/internal/engine/mysql"
	"github.com/mAmineChniti/shakedown/internal/engine/oracle"
	"github.com/mAmineChniti/shakedown/internal/engine/postgres"
	"github.com/mAmineChniti/shakedown/internal/engine/sqlite"
	"github.com/mAmineChniti/shakedown/internal/report"
	"github.com/mAmineChniti/shakedown/internal/runner"
	"github.com/mAmineChniti/shakedown/internal/synth"
)

// Version is set at release time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Println("shakedown", Version)
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("shakedown", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }

	dbEngine := fs.String("engine", "postgres", "database engine: postgres|mysql|sqlite|mssql|oracle (-e)")
	version := fs.String("version", "", "image version, e.g. 16, 8.4, mariadb:11.4, 2022-latest (-v)")
	dsn := fs.String("dsn", "", "scratch database DSN (must be local unless --allow-remote-scratch) (-d)")
	allowRemote := fs.Bool("allow-remote-scratch", false, "permit non-local scratch DSN hosts")
	baseline := fs.String("baseline", "", "schema-only SQL dump to load before migrations (-b)")
	dataDump := fs.String("data", "", "optional data SQL dump to load after the baseline (-D)")
	statsFile := fs.String("stats", "", "synthetic row counts: lines of 'table rows' (-s)")
	format := fs.String("format", "text", "output format: text|json|markdown (-f)")
	minSev := fs.String("min-severity", "warning", "minimum finding severity that fails the run: info|warning|critical (-m)")

	if err := fs.Parse(expandShorthands(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	migArgs := fs.Args()
	if len(migArgs) == 0 {
		if !stdinIsTTY() {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var err error
		migArgs, err = interactive(fs, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "shakedown: %v\n", err)
			return 2
		}
		if len(migArgs) == 0 {
			// The user cancelled with ctrl+c — exit quietly, no usage dump.
			return 0
		}
	}

	threshold, ok := parseSeverity(*minSev)
	if !ok {
		fmt.Fprintf(stderr, "shakedown: invalid --min-severity %q\n", *minSev)
		return 2
	}

	eng, err := newEngine(*dbEngine, *version, *dsn, *allowRemote)
	if err != nil {
		fmt.Fprintf(stderr, "shakedown: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := engSetup(ctx, eng, *baseline, *dataDump, *statsFile, stderr); err != nil {
		return 2
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = eng.Teardown(tctx)
	}()

	files, err := runner.LoadMigrations(migArgs)
	if err != nil {
		fmt.Fprintf(stderr, "shakedown: %v\n", err)
		return 2
	}

	r := runner.New(eng)
	results, runErr := r.Run(ctx, files)

	if err := report.Render(stdout, results, *format); err != nil {
		fmt.Fprintf(stderr, "shakedown: render: %v\n", err)
		return 2
	}

	if runErr != nil {
		// A failed migration is reported as a critical finding, not a
		// tool error — exit 1, matching the severity threshold rules.
		fmt.Fprintf(stderr, "shakedown: %v\n", runErr)
		return 1
	}
	if report.MaxSeverity(results).Rank() >= threshold.Rank() {
		return 1
	}
	return 0
}

func newEngine(name, version, dsn string, allowRemote bool) (engine.Engine, error) {
	opts := engine.Options{
		Version:            version,
		DSN:                dsn,
		AllowRemoteScratch: allowRemote,
	}
	switch engine.Dialect(name) {
	case engine.Postgres:
		opts.Engine = engine.Postgres
		return postgres.New(context.Background(), opts)
	case engine.MySQL:
		opts.Engine = engine.MySQL
		return mysql.New(context.Background(), opts)
	case engine.SQLite:
		opts.Engine = engine.SQLite
		return sqlite.New(context.Background(), opts)
	case engine.MSSQL:
		opts.Engine = engine.MSSQL
		return mssql.New(context.Background(), opts)
	case engine.Oracle:
		opts.Engine = engine.Oracle
		return oracle.New(context.Background(), opts)
	default:
		return nil, fmt.Errorf("unknown engine %q (supported: postgres, mysql, sqlite, mssql, oracle)", name)
	}
}

// engSetup loads baseline, data dump, and synthetic rows, then tears down
// its own connection. Called before migrations run.
func engSetup(ctx context.Context, eng engine.Engine, baseline, dataDump, statsFile string, stderr io.Writer) error {
	conn, err := eng.Connect(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "shakedown: connect: %v\n", err)
		return err
	}
	defer conn.Close()

	if baseline != "" {
		if err := loadSQLFile(ctx, conn, baseline); err != nil {
			fmt.Fprintf(stderr, "shakedown: baseline: %v\n", err)
			return err
		}
	}
	if dataDump != "" {
		if err := loadSQLFile(ctx, conn, dataDump); err != nil {
			fmt.Fprintf(stderr, "shakedown: data dump: %v\n", err)
			return err
		}
	}
	if statsFile != "" {
		stats, err := synth.LoadStats(statsFile)
		if err != nil {
			fmt.Fprintf(stderr, "shakedown: stats: %v\n", err)
			return err
		}
		if err := synth.Generate(ctx, conn, eng.Dialect(), stats); err != nil {
			fmt.Fprintf(stderr, "shakedown: synthetic data: %v\n", err)
			return err
		}
	}
	return nil
}

func parseSeverity(s string) (checks.Severity, bool) {
	switch checks.Severity(s) {
	case checks.Info, checks.Warning, checks.Critical:
		return checks.Severity(s), true
	}
	return "", false
}

// loadSQLFile executes a dump file statement-by-statement so multi-
// statement files and dump tools' output both work on any driver.
func loadSQLFile(ctx context.Context, db *sql.DB, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, stmt := range checks.SplitStatements(string(data)) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

const usage = `shakedown — rehearse database migrations against a throwaway database

Usage:
  shakedown [flags] <migration.sql|dir>...

Flags:
  --engine ENGINE        postgres (default), mysql, sqlite, mssql, oracle
  --version VER          image version (pg: 16|latest, mysql: 8.4,
                         mariadb:11.4, mssql: 2022-latest, oracle: 23-slim-faststart)
  --dsn DSN              use an existing scratch DB instead of Docker
  --allow-remote-scratch permit non-local DSN hosts (never use on prod)
  --baseline FILE        schema-only SQL dump to load first
  --data FILE            optional data dump to load after baseline
  --stats FILE           synthetic rows: lines of "table rows"
  --format FMT           text (default), json, or markdown
  --min-severity SEV     info|warning|critical (default warning)

Examples:
  shakedown --version 16 --baseline schema.sql migrations/
  shakedown --engine mysql --version 8.4 --data dump.sql migrations/
  shakedown --engine postgres --dsn 'postgres://u:p@localhost:5432/scratch' 001_*.sql

Exit codes: 0 clean, 1 findings >= --min-severity, 2 tool/input error.
`

// expandShorthands rewrites single-letter flags to their long forms:
// -e engine, -v version, -d dsn, -b baseline, -D data, -s stats,
// -f format, -m min-severity. Supports "-f json" and "-f=json".
func expandShorthands(args []string) []string {
	long := map[byte]string{
		'e': "engine", 'v': "version", 'd': "dsn", 'b': "baseline",
		'D': "data", 's': "stats", 'f': "format", 'm': "min-severity",
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) == 2 && a[0] == '-' {
			if name, ok := long[a[1]]; ok {
				out = append(out, "--"+name)
				continue
			}
		} else if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			if name, ok := long[a[1]]; ok && a[2] == '=' {
				out = append(out, "--"+name+"="+a[3:])
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// stdinIsTTY reports whether stdin is an interactive terminal. Checking
// ModeCharDevice is not enough: /dev/null is also a character device.
func stdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// interactive runs a guided setup wizard when shakedown is invoked bare
// on a terminal. Flags the user already passed are used as the wizard's
// defaults. It returns the migration path, or nil if the user cancelled.
func interactive(fs *flag.FlagSet, stderr io.Writer) ([]string, error) {
	flagVal := func(name string) string {
		if f := fs.Lookup(name); f != nil {
			return f.Value.String()
		}
		return ""
	}

	eng, ver := flagVal("engine"), flagVal("version")
	base, data, stats := flagVal("baseline"), flagVal("data"), flagVal("stats")
	format := flagVal("format")
	var mig string

	migrationPath := func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("choose a migration .sql file or a directory")
		}
		// Validate exactly what the runner will load, so an empty
		// directory or a down-only file fails here rather than after
		// the scratch database has been provisioned.
		_, err := runner.LoadMigrations([]string{s})
		return err
	}
	optionalFile := func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		fi, err := os.Stat(s)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory; choose a file", s)
		}
		return nil
	}
	statsPath := func(s string) error {
		if err := optionalFile(s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			return nil
		}
		_, err := synth.LoadStats(s)
		return err
	}

	// Vim-style picker keys: o/l/right descend into directories,
	// h/left/backspace go up. Enter both opens the picker and selects
	// the highlighted file or directory; esc closes it.
	km := huh.NewDefaultKeyMap()
	km.FilePicker.Open = key.NewBinding(key.WithKeys("o", "l", "right", "enter"), key.WithHelp("o/→/enter", "browse / select"))
	km.FilePicker.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select"))
	km.FilePicker.Close = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close · h/← up"))

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Database engine").
				Options(
					huh.NewOption("PostgreSQL", "postgres"),
					huh.NewOption("MySQL / MariaDB", "mysql"),
					huh.NewOption("SQLite (no Docker)", "sqlite"),
					huh.NewOption("SQL Server", "mssql"),
					huh.NewOption("Oracle", "oracle"),
				).
				Value(&eng),
			huh.NewInput().
				Title("Image version").
				Description("blank = engine default — e.g. 16, 8.4, mariadb:11.4, 2022-latest").
				Value(&ver),
		).Title("Target database").
			Description("Ctrl+C cancels the wizard."),
		huh.NewGroup(
			huh.NewFilePicker().
				Title("Migration file or directory").
				Description("an up-migration .sql file, or a directory of migrations — o/→ opens folders").
				FileAllowed(true).
				DirAllowed(true).
				Value(&mig).
				Validate(migrationPath),
		).Title("Migrations"),
		huh.NewGroup(
			huh.NewFilePicker().
				Title("Baseline schema dump").
				Description("optional — schema-only SQL of the current database — tab skips").
				FileAllowed(true).
				DirAllowed(false).
				AllowedTypes([]string{".sql"}).
				Value(&base).
				Validate(optionalFile),
			huh.NewFilePicker().
				Title("Data dump").
				Description("optional — data SQL loaded after the baseline — tab skips").
				FileAllowed(true).
				DirAllowed(false).
				AllowedTypes([]string{".sql"}).
				Value(&data).
				Validate(optionalFile),
			huh.NewFilePicker().
				Title("Stats file").
				Description("optional — 'table rows' per line for synthetic volume — tab skips").
				FileAllowed(true).
				DirAllowed(false).
				Value(&stats).
				Validate(statsPath),
		).Title("Starting state").
			Description("Optional files; press tab to skip one."),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Output format").
				Options(
					huh.NewOption("Text (colored)", "text"),
					huh.NewOption("JSON", "json"),
					huh.NewOption("Markdown", "markdown"),
				).
				Value(&format),
		).Title("Report").
			Description("The rehearsal starts after this step."),
	).
		WithKeyMap(km).
		WithOutput(stderr)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, fmt.Errorf("setup wizard: %w", err)
	}

	for _, flagValue := range []struct{ name, value string }{
		{"engine", eng},
		{"version", ver},
		{"baseline", base},
		{"data", data},
		{"stats", stats},
		{"format", format},
	} {
		if err := fs.Set(flagValue.name, flagValue.value); err != nil {
			return nil, fmt.Errorf("apply wizard value for --%s: %w", flagValue.name, err)
		}
	}
	return []string{mig}, nil
}
