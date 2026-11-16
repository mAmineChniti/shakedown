// Package engine abstracts the scratch database that migrations are
// rehearsed against. It ships Postgres and MySQL/MariaDB implementations;
// the interface is kept deliberately small so more engines can be added.
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// Dialect identifies the database engine family; checks and catalog
// queries are written per dialect.
type Dialect string

const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	SQLite   Dialect = "sqlite"
	MSSQL    Dialect = "mssql"
	Oracle   Dialect = "oracle"
)

// Options controls how the scratch database is obtained.
type Options struct {
	// Engine selects the database engine ("postgres", "mysql", or
	// "sqlite").
	Engine Dialect

	// Version selects the Docker image tag (e.g. "16" for postgres,
	// "8.4" for mysql, "11" for mariadb). Ignored when DSN is set.
	Version string

	// DSN points at an existing scratch database. The host must be local
	// (localhost, 127.0.0.1, ::1, or a unix socket) unless
	// AllowRemoteScratch is true.
	DSN string

	// AllowRemoteScratch permits non-local DSN hosts. Never use this
	// against anything resembling production.
	AllowRemoteScratch bool
}

// Engine owns the lifecycle of a disposable database.
type Engine interface {
	// Connect returns a connection pool to the scratch database.
	Connect(ctx context.Context) (*sql.DB, error)
	// Dialect reports the engine family for check/catalog selection.
	Dialect() Dialect
	// Teardown destroys the scratch database/container. Must be safe to
	// call multiple times and after partial startup failures.
	Teardown(ctx context.Context) error
}

// ValidateDSNHost refuses non-local DSN hosts unless explicitly allowed.
func ValidateDSNHost(dsn string, allowRemote bool) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil // unix socket or keyword DSN without host
	}
	local := host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "/")
	if !local && !allowRemote {
		return fmt.Errorf("refusing non-local scratch DSN host %q; pass --allow-remote-scratch to override", host)
	}
	return nil
}

// QuoteIdent quotes an identifier for the dialect. Only used for names we
// generate or read from catalogs — never for migration text.
func QuoteIdent(d Dialect, s string) string {
	switch d {
	case MySQL:
		return "`" + strings.ReplaceAll(s, "`", "``") + "`"
	case MSSQL:
		return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
	case Oracle:
		// Oracle folds unquoted identifiers to uppercase; quoting must
		// preserve that or "logs" won't resolve to LOGS.
		return `"` + strings.ToUpper(strings.ReplaceAll(s, `"`, `""`)) + `"`
	default:
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
}
