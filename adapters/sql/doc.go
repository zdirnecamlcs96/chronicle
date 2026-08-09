// Package changelogsql is a durable, database-backed implementation of
// changelog.Log, plus the optional changelog.Indexer and changelog.Deduper
// capabilities. It is the production backend for the changelog library —
// MemoryLog is reference/test only.
//
// It is written over database/sql with a small Dialect seam so other dialects
// can slot in via WithDialect, but MySQL (driver github.com/go-sql-driver/mysql)
// is the only one shipped today. It passes the same conformance suite as
// MemoryLog, ForkAppend included: AppendCommit stores the parent it is given,
// so two commits sharing a parent are a recorded fork, not an error. The row
// lock serializes only seq assignment (per-document arrival order); it does no
// concurrency control over what writers build — that belongs to the producer
// or the store the documents live in.
//
// This module carries the driver dependency in its own go.mod so the core
// changelog module stays standard-library-only.
package changelogsql
