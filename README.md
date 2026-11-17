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

A recorded terminal session is in [`demo.cast`](demo.cast) (~50s) — play it
with [asciinema](https://asciinema.org):

```bash
asciinema play demo.cast
```

![shakedown demo](demo.gif)

It tours the main features end to end:

1. **SQLite rehearsal** — three migrations against a seeded database: a
   clean add-column, a `DROP TABLE` flagged critical for losing 5,000
   rows, and a unique index that fails on real duplicate data.
2. **PostgreSQL rehearsal** — `--stats` synthetic data (50k users / 200k
   orders), observed lock modes (`ShareLock`, `AccessExclusiveLock`),
   `relfilenode` table-rewrite detection, row-count drift, and a verified
   down migration.
3. **`-f json`** — the same run as machine-readable output for CI.
4. **`-f markdown`** — a PR-comment-friendly report.

Reproduce any part yourself — `demo/` contains the baselines, migrations,
and stats file:

```bash
go run ./cmd/shakedown -e sqlite -b demo/baseline.sql demo/migrations
go run ./cmd/shakedown -e postgres -v 16 -b testdata/baseline_postgres.sql -s demo/pg_stats.txt demo/pg
go run ./cmd/shakedown -e sqlite -b demo/baseline.sql -f json demo/migrations/002_drop_logs.sql
```

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

```bash
go install github.com/mAmineChniti/shakedown/cmd/shakedown@latest
```

Requires Go 1.24+ to build. Docker is required for server engines;
SQLite and `--dsn` modes need no Docker.

## Usage

### Interactive

Running `shakedown` with no arguments on a terminal launches a guided
prompt for engine, version, migrations, baseline, and output format.

### Command line

```bash
shakedown [flags] <migration.sql|dir>...
```

| Flag | Shorthand | Description |
|---|---|---|
| `--engine` | `-e` | `postgres` (default), `mysql`, `sqlite`, `mssql`, `oracle` |
| `--version` | `-v` | Image version (`16`, `8.4`, `mariadb:11.4`, `2022-latest`, …) |
| `--dsn` | `-d` | Existing scratch database instead of Docker |
| `--allow-remote-scratch` | | Permit non-local DSN hosts |
| `--baseline` | `-b` | Schema-only SQL dump loaded before migrations |
| `--data` | `-D` | Data dump loaded after the baseline |
| `--stats` | `-s` | Synthetic row counts (`table rows` per line) |
| `--format` | `-f` | `text` (default), `json`, `markdown` |
| `--min-severity` | `-m` | `info`, `warning` (default), `critical` |

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
