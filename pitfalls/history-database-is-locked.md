# History save intermittently fails with database is locked (SQLITE_BUSY)

**Symptoms:** `database is locked (5) (SQLITE_BUSY)` during concurrent history startup.
**First seen:** 2026-09.
**Affects:** independent SQLite history Store instances opening the same new or v1 database.
**Status:** fixed in the v0.1.1 implementation.

## Symptom

The history race suite intermittently failed:

```text
$ go test -race ./internal/history ./internal/actions
--- FAIL: TestIndependentStoreInstancesCanSaveConcurrently (0.06s)
    history_test.go:289: database is locked (5) (SQLITE_BUSY)
```

## Cause and fix

Concurrent first writers and migrations could race `PRAGMA journal_mode=WAL`.
This operation does not consistently wait for the configured `busy_timeout`.
`enableWAL` now retries only `SQLITE_BUSY`, respecting cancellation and a five-second
deadline. Migrations acquire `BEGIN IMMEDIATE` before reading the schema version
again, so another opener's completed migration is observed under the write lock.

Keep these checks when changing connection setup: independent stores creating a
new database, concurrent v1 migrations, and deletion racing late saves. Targeted
race tests and ten repetitions of the concurrency cases passed after the fix.

Implementation and regression coverage: [history service](../internal/history/history.go)
and [history tests](../internal/history/history_test.go). Increasing the general
busy timeout alone does not address this WAL transition.
