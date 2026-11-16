# shakedown

[![CI](https://github.com/mAmineChniti/shakedown/actions/workflows/ci.yml/badge.svg)](https://github.com/mAmineChniti/shakedown/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Is this migration safe to ship?** shakedown rehearses your migrations
against a throwaway database and reports what *actually* happened — locks
held, table rewrites, rows affected, data that would be lost, and whether
the down migration restores the schema. Every finding carries evidence
from the real run. No guessing.

<table>
<tr><td><b>Engines</b></td><td>PostgreSQL · MySQL · MariaDB · SQLite · SQL Server</td></tr>
<tr><td><b>Provisioning</b></td><td>Docker container or local scratch DSN</td></tr>
<tr><td><b>Output</b></td><td>Colored terminal · JSON · Markdown</td></tr>
<tr><td><b>Binary</b></td><td>Single static Go binary, ~few deps</td></tr>
</table>

## Install

```bash
go install github.com/mAmineChniti/shakedown/cmd/shakedown@latest
# or
go build -o shakedown ./cmd/shakedown
```

Requires **Docker** for server engines (Postgres/MySQL/SQL Server), or any
local scratch database via `--dsn`. SQLite needs nothing at all.

## Quickstart

```bash
# Interactive — just run it on a terminal
shakedown

# Or flags (shorthands in parens)
shakedown --engine postgres --version 16 \
  --baseline schema.sql \
  migrations/

# MySQL 8 / MariaDB 11
shakedown -e mysql -v 8.4 --baseline schema.sql migrations/
shakedown -e mysql -v mariadb:11.4 migrations/

# SQLite — file-based, no Docker
shakedown -e sqlite --baseline schema.sql migrations/

# SQL Server 2022
shakedown -e mssql -v 2022-latest --baseline schema.sql migrations/
```

Got a data dump or want realistic volume? Add `--data dump.sql` or
`--stats rows.txt` (lines of `table row_count`) so checks run against
real data instead of an empty schema.

Prefer an existing scratch DB? `--dsn` works on every engine — non-local
hosts are refused unless you pass `--allow-remote-scratch`. shakedown only
ever touches the disposable database you give it.

## What you get

```
001_add_age.sql  ok  (281ms, 50000 rows affected, 4 lock observations)
    lock: AccessExclusiveLock on users (granted=true)
    [warning] CREATE INDEX idx_users_age takes a SHARE lock that blocks writes
        suggestion: use CREATE INDEX CONCURRENTLY
    [rollback/info] down migration restores the pre-migration schema
```

Per migration, shakedown reports:

| Check | What it proves |
|---|---|
| **Locks** | Lock modes actually held — sampled live from `pg_locks`, `performance_schema.metadata_locks`, or `sys.dm_tran_locks` mid-run |
| **Rewrites** | Postgres: `relfilenode` changed = table rewritten. MySQL: `ALGORITHM=COPY/INPLACE/INSTANT` analysis |
| **Data safety** | Live `count(*)` probes: values lost by `DROP COLUMN`, rows violating new `NOT NULL`/`UNIQUE`/`CHECK`/`FK`, values that won't fit a narrowed type, rows destroyed by `DROP`/`TRUNCATE` |
| **Row counts** | Before/after `count(*)` per table |
| **Rollback** | `NNN_name.down.sql` applied in reverse; normalized schema diffed against pre-migration state |

## Flags

```
-e, --engine          postgres | mysql | sqlite | mssql     (default postgres)
-v, --version         image version — pg: 16, mysql: 8.4, mariadb: 11.4,
                      mssql: 2022-latest; sqlite ignores it
-d, --dsn             scratch DB DSN instead of Docker
    --allow-remote-scratch   permit non-local DSN hosts (never on prod)
-b, --baseline        schema-only SQL dump to load first
-D, --data            optional data dump
-s, --stats           synthetic rows: lines of "table rows"
-f, --format          text (default) | json | markdown
-m, --min-severity    info | warning | critical  (default warning)
```

## Output & exit codes

- `--format text` — colored on terminals, plain when piped (`NO_COLOR` respected)
- `--format json` — machine-readable, for CI gates
- `--format markdown` — PR-ready report
- Exit `0` clean · `1` findings at/above `--min-severity` · `2` tool/input error
- Missing down migrations are reported as info, never an error

## How it compares

Phase 0 surveyed Squawk, Eugene, pgfence, pgrubic, Atlas lint, SchemaGuard,
pg-blast-radius, coloph-migrate, tendb, Sustained, and bolvrk. The short
version: static linters (Squawk, pgfence, bolvrk) are faster and catch
dangerous *patterns* without a database; dynamic tools (Eugene trace,
pgfence trace, Atlas) rehearse migrations but none combine a
**data-bearing baseline**, **row-level pre-flight counts**, and
**down-migration schema-diff verification** for arbitrary SQL files —
across four engines. shakedown's edge is proof from an actual run.

## What it does NOT catch

Honest limits — read before trusting it blindly:

- **Logic bugs.** It verifies schema/data effects, not whether your app code still works.
- **Unrecoverable data.** A down migration can't undelete a dropped column's values; rollback verification reports schema drift, not undelete.
- **Production contention.** The scratch DB has no real workload — it proves *which* locks a migration takes, not how long they'd queue in prod.
- **Dynamic SQL.** `EXECUTE` strings, PL/pgSQL, ORM-generated SQL — only the SQL text you give it is checked.
- **Per-statement rows.** Multi-statement files report summed row counts.
- **MySQL rewrite certainty.** No `relfilenode` equivalent exists; MySQL rewrite risk is inferred from `ALGORITHM` clauses, not proven.
- **SQLite locks/constraints.** No lock catalog to sample; constraints live inside `CREATE TABLE` text and aren't diffed.

## Development

```bash
go build ./cmd/shakedown
go test -short ./...                                   # unit tests, no Docker

# Integration corpora — real databases
SHAKEDOWN_ENGINE=postgres SHAKEDOWN_VERSION=16 go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=8.4  go test ./integration/
SHAKEDOWN_ENGINE=mysql    SHAKEDOWN_VERSION=mariadb:11.4 go test ./integration/
SHAKEDOWN_ENGINE=mssql    SHAKEDOWN_VERSION=2022-latest go test ./integration/
SHAKEDOWN_ENGINE=sqlite   go test ./integration/   # no Docker needed
```

Fixtures live in `testdata/corpus_<engine>/` — each case has `up.sql`, an
optional `up.down.sql`, and `expected.txt` describing required findings.
See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE) © 2026 mAmineChniti
