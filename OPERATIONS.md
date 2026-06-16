# Operating chronicle in production

This is the runbook for running a durable `chronicle` backend (SQL or
ClickHouse) under real traffic. The in-memory adapter is **dev/test only** and is
not covered here.

## Pick a backend by consistency model

| | `adapters/sql` (MySQL) | `adapters/clickhouse` |
|---|---|---|
| Consistency | **Synchronous, fork-free** | **Eventual** |
| Fork prevention | `SELECT … FOR UPDATE` on the head row + `UNIQUE(doc_id, parent)` | none — producers must serialize per-document writes |
| Dedup of a re-sent commit | immediate (`UNIQUE(doc_id, id)` / `seen` table) | at read time via `ReplacingMergeTree` + `FINAL` (until a merge runs, duplicates are visible) |
| `RunSerializableAppend` | ✅ passes | ✗ not applicable |
| Best for | source-of-truth audit log, concurrent writers per document | high-volume append, analytical queries, single serialized writer |

If two writers can seal the **same document** concurrently and you need exactly
one linear chain, use **SQL**. ClickHouse will silently keep both unless an
upstream serializes per-document writes.

## SQL (MySQL) operations

**DSN.** Must include `parseTime=true` (DATETIME scans into `time.Time`). Example:
`user:pass@tcp(host:3306)/db?parseTime=true`.

**Connection pool.** `AppendCommit` opens a transaction and holds
`SELECT … FOR UPDATE` on a document's head row for the duration of the insert.
That means concurrent appends **to the same document** queue behind the lock;
appends to **different** documents proceed in parallel. Size the pool for your
cross-document concurrency, and cap connection lifetime so the pool recycles:

```go
db, _ := sql.Open("mysql", dsn)
db.SetMaxOpenConns(n)             // ~ peak concurrent distinct-doc writers + read load
db.SetMaxIdleConns(n)
db.SetConnMaxLifetime(5 * time.Minute)
log := changelogsql.New(db, changelogsql.WithMigrate(true))
```

A hot single document is a serialization point by design — spread load across
documents, or batch a document's changes into fewer, larger commits.

**Lock waits / deadlocks.** Under contention you may see lock-wait timeouts; the
caller should treat `ErrParentConflict` as "re-read Head and re-seal" (the
`Recorder` re-chains and re-hashes). Surface neither as a 5xx.

## ClickHouse operations

**`FINAL` cost.** Every `Head`/`Commits` read uses `… FINAL`, which forces
ReplacingMergeTree dedup at query time. Cost grows with the number of unmerged
parts, so:

- Prefer **fewer, larger inserts** over many tiny ones (each insert is a part).
- Let background merges run; do not disable them.
- Reserve `OPTIMIZE TABLE commits FINAL` for maintenance windows — it rewrites
  parts and is expensive on large tables.

**Eventual dedup window.** A re-inserted identical commit (or `seen` key) is one
logical row only *after* a merge. Between insert and merge, a non-`FINAL` reader
would see duplicates — always read with `FINAL` (the adapter does).

## Migrations

`Migrate(ctx)` runs `CREATE TABLE IF NOT EXISTS` and is **idempotent** — safe to
call on every boot (`WithMigrate(true)` does this in `Open`). There is **no schema
version table yet**: additive changes are safe, but a breaking change (renaming a
column, tightening a constraint) needs a hand-written migration applied out of
band before deploying the new binary. Track this if you depend on the schema.

## Backup & restore

The `commits` table is **append-only**, which makes backup simple:

- **MySQL** — `mysqldump` or a binlog-based PITR; the append-only shape means a
  restore-then-replay is straightforward and conflict-free.
- **ClickHouse** — `BACKUP TABLE commits TO …` (native) or part-level snapshots.

Restoring is safe to over-deliver: re-inserting already-present commits dedups
(SQL by unique constraint, ClickHouse by ReplacingMergeTree).

## Observability

The adapters currently expose no metrics/tracing hooks — they are a thin layer
over `database/sql`. Instrument at the `*sql.DB` (driver-level metrics: open/idle
conns, wait count/duration) and wrap the `changelog.Log` calls in your own
spans/counters at the call site until first-class hooks land.
