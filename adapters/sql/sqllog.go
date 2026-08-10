package changelogsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/zdirnecamlcs96/chronicle/core"
)

// Log is a durable changelog.Log backed by a SQL database.
type Log struct {
	db      *sql.DB
	dialect Dialect
}

type config struct {
	dialect Dialect
	migrate bool
}

// Option configures Open/New.
type Option func(*config)

// WithDialect selects the SQL dialect (default MySQL).
func WithDialect(d Dialect) Option { return func(c *config) { c.dialect = d } }

// WithMigrate runs Migrate during Open.
func WithMigrate(m bool) Option { return func(c *config) { c.migrate = m } }

// New wraps an existing *sql.DB (e.g. a shared pool, or a test handle). Options
// that only apply to Open, such as WithMigrate, are ignored here; callers
// wrapping a pool that needs migrating should call Migrate themselves.
func New(db *sql.DB, opts ...Option) *Log {
	cfg := config{dialect: MySQL}
	for _, o := range opts {
		o(&cfg)
	}
	return &Log{db: db, dialect: cfg.dialect}
}

// Open dials dsn, pings, and (with WithMigrate) creates the schema. The MySQL
// DSN must include parseTime=true so DATETIME scans into time.Time.
func Open(ctx context.Context, dsn string, opts ...Option) (*Log, error) {
	cfg := config{dialect: MySQL}
	for _, o := range opts {
		o(&cfg)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("changelog-sql: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("changelog-sql: ping: %w", err)
	}
	l := &Log{db: db, dialect: cfg.dialect}
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

// AppendCommit stores one commit. The transaction's SELECT ... FOR UPDATE on
// the head row serializes seq assignment per document; the commit's Parent is
// stored verbatim — a writer building on a non-tip parent records a fork, not
// an error. Re-appending an identical commit (same document, same content-hash
// id) is a no-op, so an at-least-once replay lands exactly one row.
func (l *Log) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkLen("doc_id", docID); err != nil {
		return err
	}
	authors, err := json.Marshal(c.Authors)
	if err != nil {
		return fmt.Errorf("changelog-sql: marshal authors: %w", err)
	}
	changes, err := json.Marshal(c.Changes)
	if err != nil {
		return fmt.Errorf("changelog-sql: marshal changes: %w", err)
	}

	// Two writers on an EMPTY document race to seq 1: both see no head row, and
	// the FOR UPDATE gap lock cannot order inserts under it, so the loser hits
	// the (doc_id, seq) PRIMARY KEY — or an InnoDB deadlock — and re-derives a
	// fresh seq. ponytail: 5 attempts, matching the writers a gap-lock pileup
	// realistically holds; raise if a hammer test ever exhausts it.
	const maxAttempts = 5
	for attempt := 0; ; attempt++ {
		err := l.appendOnce(ctx, docID, c, authors, changes)
		switch {
		case err == nil:
			return nil
		case isDuplicateOfKey(err, "uq_commit_id"):
			// The identical commit is already stored — an at-least-once replay.
			return nil
		case attempt < maxAttempts-1 && (isDuplicateOfKey(err, "PRIMARY") || isDeadlock(err)):
			continue
		default:
			return err
		}
	}
}

// appendOnce runs one insert attempt in its own transaction. Duplicate-key
// errors come back unwrapped enough for AppendCommit to classify by index name.
func (l *Log) appendOnce(ctx context.Context, docID string, c changelog.Commit, authors, changes []byte) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("changelog-sql: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var headSeq uint64
	err = tx.QueryRowContext(ctx,
		`SELECT seq FROM commits WHERE doc_id = ? ORDER BY seq DESC LIMIT 1 FOR UPDATE`,
		docID).Scan(&headSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		headSeq = 0
	case err != nil:
		return fmt.Errorf("changelog-sql: head: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO commits (doc_id, seq, id, parent, at, authors, message, changes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		docID, headSeq+1, c.ID, c.Parent, c.At.UTC(), authors, c.Message, changes)
	if err != nil {
		return fmt.Errorf("changelog-sql: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("changelog-sql: commit: %w", err)
	}
	return nil
}

// Head returns the most recent commit ID for a document, "" if none.
func (l *Log) Head(ctx context.Context, docID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var id string
	err := l.db.QueryRowContext(ctx,
		`SELECT id FROM commits WHERE doc_id = ? ORDER BY seq DESC LIMIT 1`, docID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("changelog-sql: head: %w", err)
	}
	return id, nil
}

// Commits returns a document's commits, newest first. limit <= 0 means all.
func (l *Log) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := `SELECT id, parent, at, authors, message, changes FROM commits WHERE doc_id = ? ORDER BY seq DESC`
	args := []any{docID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-sql: commits: %w", err)
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

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanCommit(s scanner) (changelog.Commit, error) {
	var c changelog.Commit
	var authors, changes []byte
	var at time.Time
	if err := s.Scan(&c.ID, &c.Parent, &at, &authors, &c.Message, &changes); err != nil {
		return c, err
	}
	c.At = at.UTC()
	if err := json.Unmarshal(authors, &c.Authors); err != nil {
		return c, fmt.Errorf("changelog-sql: unmarshal authors: %w", err)
	}
	if err := json.Unmarshal(changes, &c.Changes); err != nil {
		return c, fmt.Errorf("changelog-sql: unmarshal changes: %w", err)
	}
	return c, nil
}

// isDuplicateOfKey reports whether err is a MySQL 1062 (ER_DUP_ENTRY) on the
// named index. The 1062 message is "Duplicate entry '<value>' for key '<index>'",
// where <value> is caller data (doc_id leads every key), so the match is anchored
// to the trailing index clause — a Contains over the whole message would let a
// doc_id spoof an index name. <index> is 'uq_commit_id' or 'commits.uq_commit_id'
// depending on MySQL version, so both the bare and table-qualified forms match.
func isDuplicateOfKey(err error, key string) bool {
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return false
	}
	i := strings.LastIndex(me.Message, "for key '")
	if i < 0 {
		return false
	}
	name := strings.Trim(me.Message[i+len("for key '"):], "'")
	return name == key || strings.HasSuffix(name, "."+key)
}

// isDeadlock reports whether err is a MySQL 1213 (ER_LOCK_DEADLOCK).
func isDeadlock(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1213
}

// maxVarcharLen is the VARCHAR(255) width of doc_id and idempotency_key (see
// schema.go). MySQL counts characters, so the bound is on runes.
const maxVarcharLen = 255

// checkLen rejects a value that would overflow its VARCHAR(255) column. Without
// it, a non-strict MySQL server silently truncates, collapsing distinct ids
// into one row.
func checkLen(field, v string) error {
	if utf8.RuneCountInString(v) > maxVarcharLen {
		return fmt.Errorf("changelog-sql: %s exceeds %d characters", field, maxVarcharLen)
	}
	return nil
}
