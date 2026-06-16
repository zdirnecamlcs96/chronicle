package changelogclickhouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/zdirnecamlcs96/chronicle/core"
)

// Log is a ClickHouse-backed changelog.Log. See the package doc for the
// append-only consistency model.
type Log struct {
	db *sql.DB
}

type config struct{ migrate bool }

// Option configures Open.
type Option func(*config)

// WithMigrate runs Migrate during Open.
func WithMigrate(m bool) Option { return func(c *config) { c.migrate = m } }

// New wraps an existing *sql.DB opened against the ClickHouse driver.
func New(db *sql.DB) *Log { return &Log{db: db} }

// Open dials a ClickHouse DSN (clickhouse://user:pass@host:9000/db), pings, and
// optionally migrates.
func Open(ctx context.Context, dsn string, opts ...Option) (*Log, error) {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, fmt.Errorf("changelog-clickhouse: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("changelog-clickhouse: ping: %w", err)
	}
	l := &Log{db: db}
	if cfg.migrate {
		if err := l.Migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return l, nil
}

// Close closes the underlying pool.
func (l *Log) Close() error { return l.db.Close() }

var _ changelog.Log = (*Log)(nil)

// AppendCommit inserts one commit. ClickHouse has no locks or unique
// constraints, so this does NOT detect parent conflicts — producers must
// serialize per-document writes. Re-inserting an identical commit is reconciled
// by ReplacingMergeTree at read time.
func (l *Log) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	authors, err := json.Marshal(c.Authors)
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: marshal authors: %w", err)
	}
	changes, err := json.Marshal(c.Changes)
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: marshal changes: %w", err)
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT INTO commits (doc_id, id, parent, at, authors, message, changes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		docID, c.ID, c.Parent, c.At.UTC(), string(authors), c.Message, string(changes))
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: insert: %w", err)
	}
	return nil
}

// Head returns the newest commit ID for a document (by timestamp).
func (l *Log) Head(ctx context.Context, docID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var id string
	err := l.db.QueryRowContext(ctx,
		`SELECT id FROM commits FINAL WHERE doc_id = ? ORDER BY at DESC, id DESC LIMIT 1`, docID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("changelog-clickhouse: head: %w", err)
	}
	return id, nil
}

// Commits returns a document's commits, newest first. limit <= 0 means all.
func (l *Log) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := `SELECT id, parent, at, authors, message, changes FROM commits FINAL WHERE doc_id = ? ORDER BY at DESC, id DESC`
	args := []any{docID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-clickhouse: commits: %w", err)
	}
	defer rows.Close()
	out := []changelog.Commit{}
	for rows.Next() {
		c, err := scanCommit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanCommit(s scanner) (changelog.Commit, error) {
	var c changelog.Commit
	var authors, changes string
	var at time.Time
	if err := s.Scan(&c.ID, &c.Parent, &at, &authors, &c.Message, &changes); err != nil {
		return c, err
	}
	c.At = at.UTC()
	if err := json.Unmarshal([]byte(authors), &c.Authors); err != nil {
		return c, fmt.Errorf("changelog-clickhouse: unmarshal authors: %w", err)
	}
	if err := json.Unmarshal([]byte(changes), &c.Changes); err != nil {
		return c, fmt.Errorf("changelog-clickhouse: unmarshal changes: %w", err)
	}
	return c, nil
}
