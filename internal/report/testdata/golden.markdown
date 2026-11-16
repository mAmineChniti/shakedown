### shakedown migration report

| Migration | Result | Duration | Rows | Findings |
|---|---|---|---|---|
| `migrations/001_add_age.sql` | ok | 123ms | 50000 | 2 |
| `migrations/002_drop.sql` | **FAILED**: ERROR: column "missing" does not exist (SQLSTATE 42703) | 4ms | 0 | 1 |

**`migrations/001_add_age.sql`**

- 🟡 **warning**: CREATE INDEX idx_users_age takes a SHARE lock that blocks writes (no CONCURRENTLY in statement)
  - Suggestion: use CREATE INDEX CONCURRENTLY
- lock observed: `AccessExclusiveLock` on `users`
- 🔵 **rollback/info**: down migration restores the pre-migration schema (normalized schema diff is empty)

**`migrations/002_drop.sql`**

- 🔴 **critical**: dropping column name discards 50000 non-null values (non-null count = 50000)

