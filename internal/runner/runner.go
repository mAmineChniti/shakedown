// Package runner applies migration files one at a time against an engine
// and collects observed evidence: duration, errors, rows affected, and the
// lock modes held (sampled from the engine's lock catalog while each
// migration runs).
package runner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mAmineChniti/shakedown/internal/checks"
	"github.com/mAmineChniti/shakedown/internal/engine"
	"github.com/mAmineChniti/shakedown/internal/schema"
)

// LockObservation is one lock mode observed on a relation while a
// migration was executing.
type LockObservation struct {
	Relation string `json:"relation"`
	Mode     string `json:"mode"`
	Granted  bool   `json:"granted"`
}

// MigrationResult records what was observed for a single migration file.
type MigrationResult struct {
	File         string            `json:"file"`
	Duration     time.Duration     `json:"duration"`
	Err          string            `json:"error,omitempty"`
	RowsAffected int64             `json:"rows_affected"`
	Locks        []LockObservation `json:"locks,omitempty"`
	Findings     []checks.Finding  `json:"findings,omitempty"`

	// DownFile is the detected down-migration path, if any.
	DownFile string `json:"down_file,omitempty"`
	// Rollback records the outcome of down-migration verification.
	Rollback []checks.Finding `json:"rollback,omitempty"`
}

// Runner applies migrations sequentially.
type Runner struct {
	eng     engine.Engine
	dialect engine.Dialect
	pre     []checks.PreCheck
	post    []checks.PostCheck
}

func New(eng engine.Engine) *Runner {
	d := eng.Dialect()
	pre, post := checks.ForDialect(d)
	return &Runner{eng: eng, dialect: d, pre: pre, post: post}
}

// LoadMigrations reads .sql files from paths (files or directories),
// sorted by name for deterministic order.
func LoadMigrations(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if isUpMigration(p) {
				files = append(files, p)
			}
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && isUpMigration(e.Name()) {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no .sql migration files found in %v", paths)
	}
	return files, nil
}

func isUpMigration(name string) bool {
	return strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql")
}

// Run applies each migration file and returns per-file results.
// Execution stops at the first failing migration. If all ups succeed,
// down migrations are applied in reverse order for rollback verification.
func (r *Runner) Run(ctx context.Context, files []string) ([]MigrationResult, error) {
	db, err := r.eng.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Pin the migration pool to ONE connection: database/sql would
	// otherwise hand each ExecContext a different backend, breaking
	// explicit BEGIN/COMMIT in migration files and invalidating the
	// backend-id lock filter.
	db.SetMaxOpenConns(1)

	samplerDB, err := r.eng.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer samplerDB.Close()
	samplerDB.SetMaxOpenConns(1)

	// Identify the migration session so the sampler can watch exactly the
	// locks this runner's statements acquire.
	migID, err := backendID(ctx, db, r.dialect)
	if err != nil {
		return nil, err
	}

	results := make([]MigrationResult, 0, len(files))
	befores := make([]map[string]schema.TableInfo, 0, len(files))
	for _, f := range files {
		sqlText, err := os.ReadFile(f)
		if err != nil {
			return results, err
		}
		before, err := schema.Snapshot(ctx, db, r.dialect)
		if err != nil {
			return results, fmt.Errorf("pre-migration snapshot for %s: %w", f, err)
		}
		befores = append(befores, before)
		res := r.runOne(ctx, db, samplerDB, migID, before, f, string(sqlText))
		results = append(results, res)
		if res.Err != "" {
			return results, fmt.Errorf("migration %s failed: %s", f, res.Err)
		}
	}

	// Rollback verification: apply down migrations in reverse order and
	// diff the resulting schema against each migration's pre-state.
	for i := len(files) - 1; i >= 0; i-- {
		r.verifyRollback(ctx, db, files[i], befores[i], &results[i])
	}
	return results, nil
}

// verifyRollback applies the down migration paired with file (if one
// exists) and diffs the schema against the pre-migration snapshot.
func (r *Runner) verifyRollback(ctx context.Context, db *sql.DB, file string, before map[string]schema.TableInfo, res *MigrationResult) {
	down := findDownFile(file)
	if down == "" {
		res.Rollback = append(res.Rollback, checks.Finding{
			Check:    "rollback",
			Severity: checks.Info,
			Message:  "no down migration found; rollback not verified",
			Evidence: "looked for " + strings.TrimSuffix(file, ".sql") + ".down.sql",
		})
		return
	}
	res.DownFile = down

	sqlText, err := os.ReadFile(down)
	if err != nil {
		res.Rollback = append(res.Rollback, checks.Finding{
			Check: "rollback", Severity: checks.Warning,
			Message: "down migration unreadable: " + err.Error(),
		})
		return
	}
	if _, err := execStatements(ctx, db, string(sqlText)); err != nil {
		res.Rollback = append(res.Rollback, checks.Finding{
			Check: "rollback", Severity: checks.Critical,
			Message:  "down migration failed to apply",
			Evidence: err.Error(),
		})
		return
	}
	after, err := schema.Snapshot(ctx, db, r.dialect)
	if err != nil {
		res.Rollback = append(res.Rollback, checks.Finding{
			Check: "rollback", Severity: checks.Warning,
			Message: "post-rollback snapshot failed: " + err.Error(),
		})
		return
	}
	diffs := schema.Diff(before, after)
	if len(diffs) == 0 {
		res.Rollback = append(res.Rollback, checks.Finding{
			Check: "rollback", Severity: checks.Info,
			Message:  "down migration restores the pre-migration schema",
			Evidence: "normalized schema diff is empty",
		})
		return
	}
	res.Rollback = append(res.Rollback, checks.Finding{
		Check: "rollback", Severity: checks.Warning,
		Message:  fmt.Sprintf("down migration leaves %d schema difference(s)", len(diffs)),
		Evidence: strings.Join(diffs, "; "),
		Suggestion: "the down migration does not fully restore the prior schema; " +
			"data already destroyed cannot be recovered",
	})
}

// findDownFile returns the down-migration path paired with an up file:
// 001_foo.sql -> 001_foo.down.sql
func findDownFile(up string) string {
	candidate := strings.TrimSuffix(up, ".sql") + ".down.sql"
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func (r *Runner) runOne(ctx context.Context, db, samplerDB *sql.DB, migID int64, before map[string]schema.TableInfo, file, sqlText string) MigrationResult {
	res := MigrationResult{File: file}
	sess := checks.Session{DB: db, Dialect: r.dialect}

	statements := checks.SplitStatements(sqlText)
	for _, pre := range r.pre {
		res.Findings = append(res.Findings, pre.Analyze(ctx, sess, statements)...)
	}

	stop := make(chan struct{})
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.sampleLocks(ctx, samplerDB, migID, stop, &mu, seen, &res)
	}()

	// Execute statements one at a time: a single multi-statement Exec is
	// wrapped in one implicit transaction on Postgres, which breaks
	// CONCURRENTLY operations and hides per-statement row counts. Explicit
	// BEGIN/COMMIT inside a file still works because the pool is pinned
	// to a single connection (SetMaxOpenConns(1) above).
	start := time.Now()
	var affected int64
	for _, stmt := range statements {
		n, err := execOne(ctx, db, stmt)
		if err != nil {
			res.Duration = time.Since(start)
			close(stop)
			wg.Wait()
			res.Err = err.Error()
			r.postChecks(ctx, db, before, &res)
			return res
		}
		affected += n
	}
	res.Duration = time.Since(start)
	close(stop)
	wg.Wait()
	res.RowsAffected = affected

	r.postChecks(ctx, db, before, &res)
	return res
}

func (r *Runner) postChecks(ctx context.Context, db *sql.DB, before map[string]schema.TableInfo, res *MigrationResult) {
	after, err := schema.Snapshot(ctx, db, r.dialect)
	if err != nil {
		return
	}
	sess := checks.Session{DB: db, Dialect: r.dialect}
	for _, post := range r.post {
		res.Findings = append(res.Findings, post.Analyze(ctx, sess, before, after)...)
	}
}

// execStatements runs each statement and returns the summed rows affected.
func execStatements(ctx context.Context, db *sql.DB, sqlText string) (int64, error) {
	var affected int64
	for _, stmt := range checks.SplitStatements(sqlText) {
		n, err := execOne(ctx, db, stmt)
		if err != nil {
			return affected, err
		}
		affected += n
	}
	return affected, nil
}

// execOne executes a single statement and returns rows affected.
func execOne(ctx context.Context, db *sql.DB, stmt string) (int64, error) {
	res, err := db.ExecContext(ctx, stmt)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // some drivers don't report counts (e.g. DDL)
	}
	return n, nil
}

// backendID returns the identifier the lock sampler filters on:
// pg_backend_pid() for Postgres, CONNECTION_ID() for MySQL. SQLite has
// no server-side lock catalog, so it returns 0 (unused).
func backendID(ctx context.Context, db *sql.DB, d engine.Dialect) (int64, error) {
	var id int64
	var q string
	switch d {
	case engine.MySQL:
		q = "SELECT CONNECTION_ID()"
	case engine.SQLite:
		return 0, nil
	case engine.MSSQL:
		q = "SELECT @@SPID"
	default:
		q = "SELECT pg_backend_pid()"
	}
	err := db.QueryRowContext(ctx, q).Scan(&id)
	return id, err
}

// sampleLocks polls the engine's lock catalog while a migration executes,
// recording distinct (relation, mode) pairs.
func (r *Runner) sampleLocks(ctx context.Context, db *sql.DB, migID int64, stop <-chan struct{}, mu *sync.Mutex, seen map[string]bool, res *MigrationResult) {
	if r.dialect == engine.SQLite {
		return // SQLite exposes no lock catalog; nothing to sample
	}
	// Sampling is inherently racy: statements faster than the interval can
	// be missed, which the report acknowledges.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var rows *sql.Rows
		var err error
		if r.dialect == engine.MSSQL {
			rows, err = db.QueryContext(ctx, `
				SELECT COALESCE(OBJECT_NAME(resource_associated_entity_id), request_mode),
				       request_mode, request_status = 'GRANT'
				FROM sys.dm_tran_locks
				WHERE request_session_id = ?
				  AND resource_type IN ('OBJECT', 'KEY', 'PAGE', 'RID', 'HOBT')`, migID)
		} else if r.dialect == engine.MySQL {
			rows, err = db.QueryContext(ctx, `
				SELECT COALESCE(ml.OBJECT_NAME, ml.LOCK_TYPE), ml.LOCK_TYPE, ml.LOCK_STATUS = 'GRANTED'
				FROM performance_schema.metadata_locks ml
				JOIN performance_schema.threads t ON t.THREAD_ID = ml.THREAD_ID
				WHERE t.PROCESSLIST_ID = ?
				  AND ml.OBJECT_TYPE IN ('TABLE', 'TABLESPACE')`, migID)
		} else {
			rows, err = db.QueryContext(ctx, `
				SELECT COALESCE(c.relname, l.locktype), l.mode, l.granted
				FROM pg_locks l
				LEFT JOIN pg_class c ON c.oid = l.relation
				WHERE l.pid = $1
				  AND l.locktype IN ('relation', 'extend', 'page', 'tuple', 'object')
				  AND l.mode <> 'AccessShareLock'`, migID)
		}
		if err != nil {
			continue
		}
		for rows.Next() {
			var rel, mode string
			var granted bool
			if err := rows.Scan(&rel, &mode, &granted); err != nil {
				continue
			}
			key := rel + "|" + mode
			mu.Lock()
			if !seen[key] {
				seen[key] = true
				res.Locks = append(res.Locks, LockObservation{Relation: rel, Mode: mode, Granted: granted})
			}
			mu.Unlock()
		}
		rows.Close()
	}
}
