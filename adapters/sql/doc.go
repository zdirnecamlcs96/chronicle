// Package changelogsql is a durable, database-backed implementation of
// changelog.Log, plus the optional changelog.Indexer and changelog.Deduper
// capabilities. It is the production backend for the changelog library —
// MemoryLog is reference/test only.
//
// It targets MySQL first (driver github.com/go-sql-driver/mysql) but is written
// over database/sql with a small Dialect seam so it ports to Postgres/SQLite.
// It passes the same conformance suite as MemoryLog — including
// the stronger RunSerializableAppend contract: concurrent same-document appends
// never fork, enforced by a per-document monotonic seq under a row lock plus a
// unique (doc_id, parent) constraint.
//
// This module carries the driver dependency in its own go.mod so the core
// changelog module stays standard-library-only.
package changelogsql
