# Contributing to shakedown

Thanks for helping make "is this migration safe?" a question with a
evidence-based answer. This guide covers setup, testing, and conventions.

## Setup

Requirements: **Go 1.27+** and **Docker** (for integration tests and the
default scratch-database engine).

```bash
git clone https://github.com/mAmineChniti/shakedown
cd shakedown
go build ./cmd/shakedown
./shakedown --help
```

## Running tests

```bash
go test -short ./...          # unit tests — no Docker needed
go vet ./... && test -z "$(gofmt -l .)"

# Integration: real databases in Docker
SHAKEDOWN_ENGINE=postgres SHAKEDOWN_VERSION=16   go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=8.4  go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=mariadb:11.4 go test ./integration/
SHAKEDOWN_ENGINE=mssql    SHAKEDOWN_VERSION=2022-latest go test ./integration/
SHAKEDOWN_ENGINE=oracle   SHAKEDOWN_VERSION=23-slim-faststart go test ./integration/
SHAKEDOWN_ENGINE=sqlite   go test ./integration/   # no Docker needed
```

`SHAKEDOWN_ENGINE` is one of `postgres`, `mysql`, `sqlite`, `mssql`,
`oracle`; `SHAKEDOWN_VERSION` is the image tag (`14`–`latest` for
Postgres, `8.4`/`mariadb:11.4` for MySQL, `2022-latest` for SQL Server,
`23-slim-faststart` for Oracle; ignored by SQLite).
CI runs the corpus across the full matrix — see `.github/workflows/ci.yml`.

## Fixture corpus

`testdata/corpus_<engine>/<NN>_<name>/`:

```
01_add_col_not_null/
  up.sql            # the migration under test
  up.down.sql       # optional down migration for rollback checks
  expected.txt      # required findings
```

`expected.txt` lines are case-insensitive substrings that must appear in
the combined finding text. Two special lines:

- `clean` — asserts no warning/critical findings and a successful run
- `error` — asserts the migration itself failed

When you add a check, add **both** a triggering fixture and a safe
counterpart. Keep fixtures small and deterministic — the shared baseline
(`testdata/baseline_<engine>.sql`) seeds ~2k users / 5k orders.

## Code layout

```
cmd/shakedown/          CLI entry — flags, engine selection, exit codes
internal/engine/        Engine iface + Options/DSN guard
internal/engine/postgres/   Docker provisioning, pgx stdlib
internal/engine/mysql/      Docker provisioning, go-sql-driver
internal/engine/sqlite/     temp-file DB, modernc.org/sqlite
internal/engine/mssql/      Docker provisioning, go-mssqldb
internal/engine/oracle/     Docker provisioning, go-ora
internal/runner/        applies migrations, samples locks, rollback verify
internal/checks/        pre-flight probes + post-run checks per dialect
internal/schema/        per-dialect catalog snapshots (pg_catalog,
                        information_schema, sqlite_master, sys.*, user_*)
                        + normalized diff for rollback verification
internal/synth/         synthetic rows from a "table rows" stats file
internal/report/        text / JSON / markdown renderers
integration/            corpus tests against real Docker databases
testdata/               baselines + per-engine fixture corpora
```

## Conventions

- **Stdlib-first.** New dependencies must be justified in the PR —
  supply-chain surface matters.
- **Evidence or silence.** A finding must come from the actual run
  (`count(*)`, `relfilenode`, lock catalog) — never from static guessing.
  If you can't measure it, don't report it.
- **Dialect-aware, not dialect-duplicated.** Shared logic lives in
  `internal/checks`; per-engine differences are explicit `switch`es on
  `engine.Dialect`, not forked packages.
- **`gofmt`, `go vet`, `go test -short ./...` must pass.** Integration
  tests need Docker; CI runs them.
- Down-migration convention is `<name>.down.sql` next to `<name>.sql`.
- Migration files and dumps are untrusted input — never execute them
  outside the database connection, never log credentials or DSNs.

## Reporting bugs

Include the migration file (sanitized), engine + version
(`--engine`/`--version`), `--format json` output, and whether it
reproduces with `--dsn` on a scratch DB.
