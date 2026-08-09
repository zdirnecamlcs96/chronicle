// Package changelogclickhouse is a durable, ClickHouse-backed implementation of
// changelog.Log plus the optional changelog.Indexer and changelog.Deduper
// capabilities — a columnar, analytics-oriented audit backend.
//
// Consistency note: AppendCommit stores the parent it is given — two commits
// sharing a parent are a recorded fork, which is the Log contract, not a
// defect (conformance.RunLogConformance's ForkAppend covers it). ClickHouse
// has no locks or transactions, so per-document ordering rests on the `at`
// timestamp: producer clock skew reorders arrival, which shifts the
// last-write-wins fold. Storage-level idempotency uses ReplacingMergeTree
// (dedup of identical commits / seen keys), made consistent at read time with
// FINAL; rows are deduped only when doc_id, at, AND id all match — the same
// commit re-appended with a different at persists as a duplicate row.
//
// This adapter exists partly to PROVE genericity: the same conformance suite
// that validates MemoryLog and the SQL backend also validates a columnar store
// with a fundamentally different consistency model.
//
// Driver: github.com/ClickHouse/clickhouse-go/v2 via database/sql. The core
// changelog module stays dependency-free; this adapter carries the driver.
package changelogclickhouse
