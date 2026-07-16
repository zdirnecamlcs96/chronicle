package changelogsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zdirnecamlcs96/chronicle/core"
)

// The SQL backend answers cross-document queries and idempotency natively, so a
// SQL-backed server's global reads and dedup are durable (survive a restart).
var (
	_ changelog.Indexer     = (*Log)(nil)
	_ changelog.Deduper     = (*Log)(nil)
	_ changelog.TailReader  = (*Log)(nil)
	_ changelog.Snapshotter = (*Log)(nil)
)

// AllCommits returns commits across all documents, newest first.
func (l *Log) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := `SELECT doc_id, id, parent, at, authors, message, changes FROM commits ORDER BY at DESC, doc_id ASC, seq DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-sql: all commits: %w", err)
	}
	defer rows.Close()
	out := []changelog.DocCommit{}
	for rows.Next() {
		dc, err := scanDocCommit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dc)
	}
	return out, rows.Err()
}

// FindByID returns the commit with the given id and its document. Commit IDs are
// content hashes scoped per-document (see schema.go), so on the rare occasion
// two documents share an id, the first match wins.
func (l *Log) FindByID(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.DocCommit{}, false, err
	}
	row := l.db.QueryRowContext(ctx,
		`SELECT doc_id, id, parent, at, authors, message, changes FROM commits WHERE id = ? LIMIT 1`, commitID)
	dc, err := scanDocCommit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.DocCommit{}, false, nil
	}
	if err != nil {
		return changelog.DocCommit{}, false, err
	}
	return dc, true, nil
}

// Seen returns the commit (docID, key) previously sealed. The lookup and the
// commit fetch are both scoped to docID, so a key reused on another document
// never returns that document's commit.
func (l *Log) Seen(ctx context.Context, docID, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	var commitID string
	err := l.db.QueryRowContext(ctx,
		`SELECT commit_id FROM seen WHERE doc_id = ? AND idempotency_key = ?`, docID, key).Scan(&commitID)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Commit{}, false, nil
	}
	if err != nil {
		return changelog.Commit{}, false, fmt.Errorf("changelog-sql: seen: %w", err)
	}
	row := l.db.QueryRowContext(ctx,
		`SELECT doc_id, id, parent, at, authors, message, changes FROM commits WHERE doc_id = ? AND id = ? LIMIT 1`,
		docID, commitID)
	dc, err := scanDocCommit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Commit{}, false, nil
	}
	if err != nil {
		return changelog.Commit{}, false, fmt.Errorf("changelog-sql: seen commit: %w", err)
	}
	return dc.Commit, true, nil
}

// MarkSeen records that key sealed commit c for docID. First (docID, key) writer wins.
func (l *Log) MarkSeen(ctx context.Context, docID, key string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkLen("doc_id", docID); err != nil {
		return err
	}
	if err := checkLen("idempotency_key", key); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO seen (idempotency_key, doc_id, commit_id, at) VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE idempotency_key = idempotency_key`,
		key, docID, c.ID, c.At.UTC())
	if err != nil {
		return fmt.Errorf("changelog-sql: mark seen: %w", err)
	}
	return nil
}

// CommitsAfter returns docID's commits strictly after afterID, oldest first
// (replay order). afterID "" means from the root. limit <= 0 means all. An
// afterID not on the document returns changelog.ErrNoSuchCommit.
func (l *Log) CommitsAfter(ctx context.Context, docID, afterID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var afterSeq uint64
	if afterID != "" {
		err := l.db.QueryRowContext(ctx,
			`SELECT seq FROM commits WHERE doc_id = ? AND id = ?`, docID, afterID).Scan(&afterSeq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, changelog.ErrNoSuchCommit
		}
		if err != nil {
			return nil, fmt.Errorf("changelog-sql: commits after: %w", err)
		}
	}
	q := `SELECT id, parent, at, authors, message, changes FROM commits WHERE doc_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{docID, afterSeq}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-sql: commits after: %w", err)
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

// SaveSnapshot stores s, replacing any prior snapshot for s.DocID (latest write
// wins via ON DUPLICATE KEY UPDATE against the doc_id primary key).
func (l *Log) SaveSnapshot(ctx context.Context, s changelog.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkLen("doc_id", s.DocID); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO snapshots (doc_id, commit_id, state, at) VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE commit_id = VALUES(commit_id), state = VALUES(state), at = VALUES(at)`,
		s.DocID, s.CommitID, s.State, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("changelog-sql: save snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot returns docID's stored snapshot; ok is false if none.
func (l *Log) LoadSnapshot(ctx context.Context, docID string) (changelog.Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Snapshot{}, false, err
	}
	s := changelog.Snapshot{DocID: docID}
	err := l.db.QueryRowContext(ctx,
		`SELECT commit_id, state FROM snapshots WHERE doc_id = ?`, docID).Scan(&s.CommitID, &s.State)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Snapshot{}, false, nil
	}
	if err != nil {
		return changelog.Snapshot{}, false, fmt.Errorf("changelog-sql: load snapshot: %w", err)
	}
	return s, true, nil
}

// pruneSeenBatch caps each PruneSeen DELETE so a large backlog doesn't hold a
// long-running lock.
// ponytail: fixed batch size; make it an option if a deployment ever needs tuning.
const pruneSeenBatch = 10000

// PruneSeen deletes seen rows older than olderThan, in batches, and returns the
// total rows deleted. Operators cron this; there is no automatic TTL, so
// retention must exceed the producer's max redelivery window, or a legitimate
// retry can slip past dedup and be treated as a new delivery.
func (l *Log) PruneSeen(ctx context.Context, olderThan time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var total int64
	for {
		res, err := l.db.ExecContext(ctx,
			`DELETE FROM seen WHERE at < ? LIMIT ?`, olderThan.UTC(), pruneSeenBatch)
		if err != nil {
			return total, fmt.Errorf("changelog-sql: prune seen: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("changelog-sql: prune seen: %w", err)
		}
		total += n
		if n == 0 {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func scanDocCommit(s scanner) (changelog.DocCommit, error) {
	var dc changelog.DocCommit
	var authors, changes []byte
	var at time.Time
	if err := s.Scan(&dc.DocID, &dc.Commit.ID, &dc.Commit.Parent, &at, &authors, &dc.Commit.Message, &changes); err != nil {
		return dc, err
	}
	dc.Commit.At = at.UTC()
	if err := json.Unmarshal(authors, &dc.Commit.Authors); err != nil {
		return dc, fmt.Errorf("changelog-sql: unmarshal authors: %w", err)
	}
	if err := json.Unmarshal(changes, &dc.Commit.Changes); err != nil {
		return dc, fmt.Errorf("changelog-sql: unmarshal changes: %w", err)
	}
	return dc, nil
}
