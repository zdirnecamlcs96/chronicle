package changelogclickhouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/zdirnecamlcs96/chronicle/core"
)

// Log is a ClickHouse-backed changelog.Log. See the package doc for the
// append-only consistency model.
type Log struct {
	db *sql.DB
	t  tables
}

// tables names the four tables the adapter reads and writes, derived from a
// caller-supplied prefix (see New/Open).
type tables struct {
	Commits     string
	Seen        string
	Snapshots   string
	Annotations string
}

var tableNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// newTables validates prefix and derives the four table names from it: a
// trailing underscore is stripped, then the prefix is joined with a fixed
// "_changelog_" segment and the table name (e.g. "myapp" ->
// myapp_changelog_commits).
func newTables(prefix string) (tables, error) {
	prefix = strings.TrimSuffix(prefix, "_")
	if prefix == "" {
		return tables{}, errors.New("changelog-clickhouse: table prefix is required")
	}
	if !tableNameRe.MatchString(prefix) {
		return tables{}, fmt.Errorf("changelog-clickhouse: invalid table prefix %q: must match %s", prefix, tableNameRe.String())
	}
	return tables{
		Commits:     prefix + "_changelog_commits",
		Seen:        prefix + "_changelog_seen",
		Snapshots:   prefix + "_changelog_snapshots",
		Annotations: prefix + "_changelog_annotations",
	}, nil
}

type config struct{ migrate bool }

// Option configures Open.
type Option func(*config)

// WithMigrate runs Migrate during Open.
func WithMigrate(m bool) Option { return func(c *config) { c.migrate = m } }

// New wraps an existing *sql.DB opened against the ClickHouse driver. prefix
// is required and names the four tables <prefix>_changelog_commits,
// <prefix>_changelog_seen, <prefix>_changelog_snapshots,
// <prefix>_changelog_annotations (e.g. "myapp" -> myapp_changelog_commits). A
// trailing underscore on prefix is stripped before joining.
func New(db *sql.DB, prefix string) (*Log, error) {
	t, err := newTables(prefix)
	if err != nil {
		return nil, err
	}
	return &Log{db: db, t: t}, nil
}

// Open dials a ClickHouse DSN (clickhouse://user:pass@host:9000/db), pings, and
// optionally migrates. prefix is required and names the four tables
// <prefix>_changelog_commits, <prefix>_changelog_seen,
// <prefix>_changelog_snapshots, <prefix>_changelog_annotations (e.g. "myapp"
// -> myapp_changelog_commits). A trailing underscore on prefix is stripped
// before joining.
func Open(ctx context.Context, dsn string, prefix string, opts ...Option) (*Log, error) {
	t, err := newTables(prefix)
	if err != nil {
		return nil, err
	}
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
	l := &Log{db: db, t: t}
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
		fmt.Sprintf(`INSERT INTO %s (doc_id, id, parent, at, authors, message, changes, sig_key_id, signature) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, l.t.Commits),
		docID, c.ID, c.Parent, c.At.UTC(), string(authors), c.Message, string(changes), c.SigKeyID, string(c.Signature))
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
		fmt.Sprintf(`SELECT id FROM %s FINAL WHERE doc_id = ? ORDER BY at DESC, id DESC LIMIT 1`, l.t.Commits), docID).Scan(&id)
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
	q := fmt.Sprintf(`SELECT id, parent, at, authors, message, changes, sig_key_id, signature FROM %s FINAL WHERE doc_id = ? ORDER BY at DESC, id DESC`, l.t.Commits)
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
	var authors, changes, sigKeyID, signature string
	var at time.Time
	if err := s.Scan(&c.ID, &c.Parent, &at, &authors, &c.Message, &changes, &sigKeyID, &signature); err != nil {
		return c, err
	}
	c.At = at.UTC()
	c.SigKeyID = sigKeyID
	if signature != "" {
		c.Signature = []byte(signature)
	}
	if err := json.Unmarshal([]byte(authors), &c.Authors); err != nil {
		return c, fmt.Errorf("changelog-clickhouse: unmarshal authors: %w", err)
	}
	if err := json.Unmarshal([]byte(changes), &c.Changes); err != nil {
		return c, fmt.Errorf("changelog-clickhouse: unmarshal changes: %w", err)
	}
	return c, nil
}
