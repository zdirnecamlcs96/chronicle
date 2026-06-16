// Package changelogclickhouse is a durable, ClickHouse-backed implementation of
// changelog.Log plus the optional changelog.Indexer and changelog.Deduper
// capabilities — a columnar, analytics-oriented audit backend.
//
// Consistency note (important): ClickHouse is append-only and has no row locks,
// transactions, or enforced unique constraints, so this adapter CANNOT prevent
// forks under concurrent same-document appends the way the SQL adapter does. It
// passes conformance.RunLogConformance (the mandatory Log contract) but NOT
// RunSerializableAppend — producers must serialize writes per document (e.g. a
// single-writer-per-doc relay) or accept eventual reconciliation. Storage-level
// idempotency uses ReplacingMergeTree (dedup of identical commits / seen keys),
// made consistent at read time with FINAL.
//
// This adapter exists partly to PROVE genericity: the same conformance suite
// that validates MemoryLog and the SQL backend also validates a columnar store
// with a fundamentally different consistency model.
//
// Driver: github.com/ClickHouse/clickhouse-go/v2 via database/sql. The core
// changelog module stays dependency-free; this adapter carries the driver.
package changelogclickhouse
