package changelogclickhouse

import (
	"context"
	"fmt"
)

// ReplacingMergeTree dedups rows with an identical ORDER BY key during
// background merges; reads use FINAL to force that dedup at query time. A
// re-inserted identical commit (same doc_id, at, id) or seen key collapses to
// one row — the columnar equivalent of the SQL adapter's unique constraints,
// but EVENTUAL rather than synchronous (hence no fork-prevention).
const commitsDDL = `CREATE TABLE IF NOT EXISTS commits (
	doc_id   String,
	id       String,
	parent   String,
	at       DateTime64(6),
	authors  String,
	message  String,
	changes  String
) ENGINE = ReplacingMergeTree
ORDER BY (doc_id, at, id)`

const seenDDL = `CREATE TABLE IF NOT EXISTS seen (
	idempotency_key String,
	doc_id          String,
	commit_id       String,
	at              DateTime64(6)
) ENGINE = ReplacingMergeTree
ORDER BY idempotency_key`

// Migrate creates the schema if absent. Safe to call on every startup.
func (l *Log) Migrate(ctx context.Context) error {
	for _, ddl := range []string{commitsDDL, seenDDL} {
		if _, err := l.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("changelog-clickhouse: migrate: %w", err)
		}
	}
	return nil
}
