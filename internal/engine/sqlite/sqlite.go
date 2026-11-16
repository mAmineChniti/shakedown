// Package sqlite implements engine.Engine for SQLite. There is no server
// to provision: the scratch database is a temporary file that Teardown
// deletes. modernc.org/sqlite is pure Go, keeping the binary static.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

// Engine is a SQLite-backed engine.Engine backed by a temp file.
type Engine struct {
	opts     engine.Options
	path     string
	external bool
}

// New creates a temporary database file, or validates a user-supplied
// file path via Options.DSN.
func New(_ context.Context, opts engine.Options) (*Engine, error) {
	e := &Engine{opts: opts}
	if opts.DSN != "" {
		e.external = true
		e.path = opts.DSN
		return e, nil
	}
	dir, err := os.MkdirTemp("", "shakedown-")
	if err != nil {
		return nil, err
	}
	e.path = filepath.Join(dir, "scratch.db")
	return e, nil
}

// Connect implements engine.Engine.
func (e *Engine) Connect(_ context.Context) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", e.path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Dialect implements engine.Engine.
func (e *Engine) Dialect() engine.Dialect { return engine.SQLite }

// Teardown implements engine.Engine.
func (e *Engine) Teardown(_ context.Context) error {
	if e.external {
		return nil
	}
	if e.path == "" {
		return nil
	}
	path := e.path
	e.path = ""
	// Remove db plus WAL/SHM sidecars and the temp dir.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	_ = os.Remove(filepath.Dir(path))
	return nil
}
