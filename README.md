# shakedown

[![CI](https://github.com/mAmineChniti/shakedown/actions/workflows/ci.yml/badge.svg)](https://github.com/mAmineChniti/shakedown/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Migration rehearsal for databases.** shakedown executes your migrations
against a disposable database — provisioned in Docker or supplied via DSN —
and reports what actually happened: locks held, table rewrites, rows
affected, data-loss exposure, and whether the down migration restores the
schema. Every finding is backed by evidence collected during the run.

## Demo

![shakedown demo](demo.gif)

## Supported engines

| Engine | Versions | Scratch provisioning | Lock observation | Rewrite detection |
|---|---|---|---|---|
| PostgreSQL | 14+ (`--version 16`, `latest`, …) | Docker `postgres:X` or DSN | `pg_locks` sampling | `relfilenode` diff |
| MySQL / MariaDB | 8.x, 11.x (`8.4`, `mariadb:11.4`) | Docker `mysql:X`/`mariadb:X` or DSN | `performance_schema.metadata_locks` | `ALGORITHM=` analysis |
| SQLite | any | temp file (no Docker) | — | — |
| SQL Server | 2019/2022 (`2022-latest`) | Docker `mssql/server:X` or DSN | `sys.dm_tran_locks` | — |
| Oracle | 23c (`23-slim-faststart`) | Docker `oracle-free:X` or DSN | — | — |

## What it checks

- **Lock exposure** — samples the engine's lock catalog while each
  migration executes and reports the lock modes actually acquired.
- **Table rewrites** — Postgres: a changed `relfilenode` is proof of a
  rewrite. MySQL: `ALGORITHM=COPY`/`INPLACE`/`INSTANT` clauses are analyzed
  and reported.
- **Data safety** — live `count(*)` probes executed before each migration:
  values discarded by `DROP COLUMN`, rows that would violate new
  `NOT NULL`/`UNIQUE`/`CHECK`/foreign-key constraints, values that cannot
  survive a narrowed type, and rows destroyed by `DROP`/`TRUNCATE`.
- **Row counts** — before/after `count(*)` for every user table.
- **Rollback** — `NNN_name.down.sql` files are applied in reverse order and
  the resulting schema is diffed against the pre-migration state
  (tables, columns, indexes, constraints). Missing down migrations are
  reported as informational, not errors.

## Install

**Prebuilt binaries** — grab the binary for your platform from
[Releases](https://github.com/mAmineChniti/shakedown/releases)
(Linux, macOS, Windows × amd64/arm64; `.sha256` checksums included):

```bash
chmod +x shakedown_linux_amd64
./shakedown_linux_amd64 version
```

**From source** — requires Go 1.24+:

```bash
go install github.com/mAmineChniti/shakedown/cmd/shakedown@latest
```

Docker is required for server engines; SQLite and `--dsn` modes need
no Docker.

## Usage

### Interactive

Running `shakedown` with no arguments on a terminal launches a guided
wizard for the engine/version, migration path, optional baseline/data/
stats files, and output format. Arrow keys move, enter opens a folder or
selects a file, `o`/`→` opens folders, and tab skips an optional picker.
Flags you already passed are used as the defaults; ctrl+c cancels.

### Command line

```bash
shakedown [flags] <migration.sql|dir>...
```

The positional argument is **the migrations you want to test** — a single
`.sql` file or a directory of them (applied in filename order; `*.down.sql`
files are used for rollback verification, not run as migrations).

The flags build the **starting state** the migrations run against:

| Flag | Shorthand | What it is |
|---|---|---|
| `--engine` | `-e` | Database engine: `postgres` (default), `mysql`, `sqlite`, `mssql`, `oracle` |
| `--version` | `-v` | Engine version / Docker image tag (`16`, `8.4`, `mariadb:11.4`, `2022-latest`, …) |
| `--baseline` | `-b` | **Schema dump of your current database** — `pg_dump --schema-only`, `mysqldump --no-data`, etc. Loaded first so migrations run against your real tables |
| `--data` | `-D` | Optional **data dump** loaded after the baseline |
| `--stats` | `-s` | **Synthetic volume** instead of a data dump: a text file with `table_name row_count` per line (e.g. `orders 500000`). shakedown generates that many rows per table |
| `--dsn` | `-d` | Use an existing scratch database instead of provisioning one |
| `--allow-remote-scratch` | | Permit non-local `--dsn` hosts (never use on production) |
| `--format` | `-f` | Output: `text` (default, colored), `json`, `markdown` |
| `--min-severity` | `-m` | Lowest severity that fails the run: `info`, `warning` (default), `critical` |

So this command:

```bash
shakedown -e mysql -v 8.4 -b schema.sql -s rows.txt migrations/
```

means: *spin up MySQL 8.4 → load `schema.sql` as the current schema →
generate the row counts in `rows.txt` → run every migration in
`migrations/` → report locks, rewrites, data loss, and rollback results.*

```bash
# PostgreSQL 16 with a schema dump
shakedown -e postgres -v 16 -b schema.sql migrations/

# MySQL 8 with synthetic volume
shakedown -e mysql -v 8.4 -b schema.sql -s rows.txt migrations/

# Existing scratch database
shakedown -d 'postgres://u:p@localhost:5432/scratch' migrations/

# CI-friendly machine output
shakedown -f json migrations/ > report.json
```

### Output

Text output is colorized on terminals and plain when piped (`NO_COLOR`
is respected). `--format markdown` produces a PR-ready report.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | No findings at or above `--min-severity` |
| `1` | Findings at or above `--min-severity`, or a migration failed |
| `2` | Tool or input error |

## Design notes

- **Evidence over heuristics.** A finding is only reported when the
  rehearsal produced observable evidence (row counts, lock catalog rows,
  catalog diffs). Static pattern-matching is deliberately not the source
  of truth.
- **Disposable by construction.** Docker engines run in throwaway
  containers on loopback with random names/ports; teardown happens on
  exit, interrupt, and failure paths. `--dsn` hosts must be local unless
  `--allow-remote-scratch` is passed explicitly.
- **Untrusted input.** Migration files and dumps are only ever executed
  through the database connection; credentials and DSNs are never logged.
- **Per-engine dialects.** Catalog queries, lock sources, and type-cast
  probes are implemented per engine behind `engine.Dialect`; there is no
  pretend-universal SQL.

## Known limitations

- Verifies schema/data effects, not application logic.
- Rollback verification reports schema drift; it cannot undelete data.
- Scratch databases have no production workload — lock *modes* are
  proven, lock *wait times* are not.
- Dynamic SQL (PL/pgSQL, `EXECUTE` strings, ORM-generated SQL) is not
  analyzed.
- Multi-statement files report summed row counts.
- MySQL has no `relfilenode` equivalent; rewrite risk is inferred from
  `ALGORITHM` clauses. SQLite and Oracle have no sampled lock source.
- Oracle does not yet support `--stats` synthetic data.

## Development

```bash
go build ./cmd/shakedown
go test -short ./...                                   # unit tests

# Integration corpora — real databases in Docker
SHAKEDOWN_ENGINE=postgres SHAKEDOWN_VERSION=16 go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=8.4  go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=mariadb:11.4 go test ./integration/
SHAKEDOWN_ENGINE=mssql    SHAKEDOWN_VERSION=2022-latest go test ./integration/
SHAKEDOWN_ENGINE=sqlite   go test ./integration/
SHAKEDOWN_ENGINE=oracle   SHAKEDOWN_VERSION=23-slim-faststart go test ./integration/
```

Fixture corpora live in `testdata/corpus_<engine>/` (`up.sql`, optional
`up.down.sql`, `expected.txt`).

## Contributing

Issues, bug reports, new engines, and check improvements are welcome.
Setup, test commands, fixture-corpus conventions, and code-layout notes
are in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE) © 2026 mAmineChniti
