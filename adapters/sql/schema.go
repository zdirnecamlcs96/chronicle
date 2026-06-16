package changelogsql

import (
	"context"
	"fmt"
)

// Dialect captures the small SQL differences between supported databases.
// MySQL is the tested target; the seam exists so Postgres/SQLite can be added
// without touching the Log logic.
type Dialect int

const (
	// MySQL is the default, tested dialect.
	MySQL Dialect = iota
)

// ddl returns the CREATE TABLE statements for the dialect, idempotent
// (IF NOT EXISTS) so Migrate can run on every boot.
//
// The schema encodes correctness as constraints, independent of any
// application locking:
//   - PRIMARY KEY (doc_id, seq): per-document ordering; the lock target.
//   - UNIQUE (doc_id, id): per-document commit-id uniqueness. A commit ID is a
//     content hash of (parent, message, changes) — it deliberately excludes the
//     document, so two DIFFERENT documents can legitimately carry the same id
//     when their content matches (a content-addressed, git-style chain). The
//     uniqueness is therefore scoped to the document, not global; KEY idx_id
//     keeps FindByID fast despite id no longer being a leftmost unique column.
//   - UNIQUE (doc_id, parent): the anti-fork rule — at most one child per
//     parent, and exactly one root (parent='') per document.
func (d Dialect) ddl() []string {
	switch d {
	default: // MySQL
		return []string{
			`CREATE TABLE IF NOT EXISTS commits (
				doc_id   VARCHAR(255)    NOT NULL,
				seq      BIGINT UNSIGNED NOT NULL,
				id       CHAR(64)        NOT NULL,
				parent   CHAR(64)        NOT NULL DEFAULT '',
				at       DATETIME(6)     NOT NULL,
				authors  JSON            NOT NULL,
				message  TEXT            NOT NULL,
				changes  JSON            NOT NULL,
				PRIMARY KEY (doc_id, seq),
				UNIQUE KEY uq_commit_id (doc_id, id),
				UNIQUE KEY uq_doc_parent (doc_id, parent),
				KEY idx_id (id),
				KEY idx_at (at),
				KEY idx_doc_at (doc_id, at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			`CREATE TABLE IF NOT EXISTS seen (
				idempotency_key VARCHAR(255) NOT NULL,
				doc_id          VARCHAR(255) NOT NULL,
				commit_id       CHAR(64)     NOT NULL,
				at              DATETIME(6)  NOT NULL,
				PRIMARY KEY (idempotency_key)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		}
	}
}

// Migrate creates the schema if absent. Safe to call on every startup.
func (l *Log) Migrate(ctx context.Context) error {
	for _, stmt := range l.dialect.ddl() {
		if _, err := l.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("changelog-sql: migrate: %w", err)
		}
	}
	return nil
}
