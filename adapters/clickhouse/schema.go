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
// sig_key_id/signature default to "" (no NULL in ReplacingMergeTree without an
// explicit Nullable wrapper): an unsigned commit's zero value round-trips as
// the empty string / empty bytes, matching core.Commit's own zero value.
func commitsDDL(name string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	doc_id     String,
	id         String,
	parent     String,
	at         DateTime64(6),
	authors    String,
	message    String,
	changes    String,
	sig_key_id String,
	signature  String
) ENGINE = ReplacingMergeTree
ORDER BY (doc_id, at, id)`, name)
}

func seenDDL(name string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	idempotency_key String,
	doc_id          String,
	commit_id       String,
	at              DateTime64(6)
) ENGINE = ReplacingMergeTree
ORDER BY (doc_id, idempotency_key)`, name)
}

// snapshotsDDL is a pure cache (a reader always has the full replay as
// fallback), so TRUNCATE is always safe. `at` is the version column: on
// merge/FINAL the newest save wins, the eventual-consistency analogue of an
// upsert keyed by doc_id.
func snapshotsDDL(name string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	doc_id    String,
	commit_id String,
	state     String,
	at        DateTime64(6)
) ENGINE = ReplacingMergeTree(at)
ORDER BY (doc_id)`, name)
}

// annotationsDDL holds per-commit display sidecars OUTSIDE the hash seal, one
// per (doc, commit), latest wins via the `at` version column. Non-authoritative:
// TRUNCATE is always safe, readers fall back to live decoration.
func annotationsDDL(name string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	doc_id    String,
	commit_id String,
	data      String,
	at        DateTime64(6)
) ENGINE = ReplacingMergeTree(at)
ORDER BY (doc_id, commit_id)`, name)
}

// Migrate creates the schema if absent and upgrades an existing commits table
// with the sig_key_id/signature columns. Safe to call on every startup.
func (l *Log) Migrate(ctx context.Context) error {
	for _, ddl := range []string{
		commitsDDL(l.t.Commits),
		seenDDL(l.t.Seen),
		snapshotsDDL(l.t.Snapshots),
		annotationsDDL(l.t.Annotations),
	} {
		if _, err := l.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("changelog-clickhouse: migrate: %w", err)
		}
	}
	if _, err := l.db.ExecContext(ctx,
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS sig_key_id String, ADD COLUMN IF NOT EXISTS signature String`, l.t.Commits)); err != nil {
		return fmt.Errorf("changelog-clickhouse: migrate signature columns: %w", err)
	}
	return nil
}
