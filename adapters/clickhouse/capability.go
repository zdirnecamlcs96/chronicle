package changelogclickhouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zdirnecamlcs96/chronicle/core"
)

var (
	_ changelog.Indexer = (*Log)(nil)
	_ changelog.Deduper = (*Log)(nil)
)

// AllCommits returns commits across all documents, newest first.
func (l *Log) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := `SELECT doc_id, id, parent, at, authors, message, changes FROM commits FINAL ORDER BY at DESC, doc_id, id`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-clickhouse: all commits: %w", err)
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

// FindByID returns the commit with the given id and its document.
func (l *Log) FindByID(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.DocCommit{}, false, err
	}
	row := l.db.QueryRowContext(ctx,
		`SELECT doc_id, id, parent, at, authors, message, changes FROM commits FINAL WHERE id = ? LIMIT 1`, commitID)
	dc, err := scanDocCommit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.DocCommit{}, false, nil
	}
	if err != nil {
		return changelog.DocCommit{}, false, err
	}
	return dc, true, nil
}

// Seen returns the commit a key previously sealed.
func (l *Log) Seen(ctx context.Context, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	var commitID string
	err := l.db.QueryRowContext(ctx,
		`SELECT commit_id FROM seen FINAL WHERE idempotency_key = ? LIMIT 1`, key).Scan(&commitID)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Commit{}, false, nil
	}
	if err != nil {
		return changelog.Commit{}, false, fmt.Errorf("changelog-clickhouse: seen: %w", err)
	}
	dc, ok, err := l.FindByID(ctx, commitID)
	if err != nil {
		return changelog.Commit{}, false, err
	}
	return dc.Commit, ok, nil
}

// MarkSeen records that key sealed commit c. ReplacingMergeTree reconciles
// re-inserts of the same key at read time.
func (l *Log) MarkSeen(ctx context.Context, key, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO seen (idempotency_key, doc_id, commit_id, at) VALUES (?, ?, ?, ?)`,
		key, docID, c.ID, c.At.UTC())
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: mark seen: %w", err)
	}
	return nil
}

func scanDocCommit(s scanner) (changelog.DocCommit, error) {
	var dc changelog.DocCommit
	var authors, changes string
	var at time.Time
	if err := s.Scan(&dc.DocID, &dc.Commit.ID, &dc.Commit.Parent, &at, &authors, &dc.Commit.Message, &changes); err != nil {
		return dc, err
	}
	dc.Commit.At = at.UTC()
	if err := json.Unmarshal([]byte(authors), &dc.Commit.Authors); err != nil {
		return dc, fmt.Errorf("changelog-clickhouse: unmarshal authors: %w", err)
	}
	if err := json.Unmarshal([]byte(changes), &dc.Commit.Changes); err != nil {
		return dc, fmt.Errorf("changelog-clickhouse: unmarshal changes: %w", err)
	}
	return dc, nil
}
