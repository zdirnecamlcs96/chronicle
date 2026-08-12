package changelogclickhouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zdirnecamlcs96/chronicle/core"
)

var (
	_ changelog.Indexer     = (*Log)(nil)
	_ changelog.Deduper     = (*Log)(nil)
	_ changelog.TailReader  = (*Log)(nil)
	_ changelog.Snapshotter = (*Log)(nil)
	_ changelog.Annotator   = (*Log)(nil)
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

// Seen returns the commit (docID, key) previously sealed. Both the lookup and
// the commit fetch are scoped to docID, so a key reused on another document
// never returns that document's commit.
func (l *Log) Seen(ctx context.Context, docID, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	var commitID string
	err := l.db.QueryRowContext(ctx,
		`SELECT commit_id FROM seen FINAL WHERE doc_id = ? AND idempotency_key = ? LIMIT 1`, docID, key).Scan(&commitID)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Commit{}, false, nil
	}
	if err != nil {
		return changelog.Commit{}, false, fmt.Errorf("changelog-clickhouse: seen: %w", err)
	}
	row := l.db.QueryRowContext(ctx,
		`SELECT doc_id, id, parent, at, authors, message, changes FROM commits FINAL WHERE doc_id = ? AND id = ? LIMIT 1`,
		docID, commitID)
	dc, err := scanDocCommit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Commit{}, false, nil
	}
	if err != nil {
		return changelog.Commit{}, false, fmt.Errorf("changelog-clickhouse: seen commit: %w", err)
	}
	return dc.Commit, true, nil
}

// MarkSeen records that key sealed commit c for docID. ReplacingMergeTree
// reconciles re-inserts of the same (docID, key) at read time.
func (l *Log) MarkSeen(ctx context.Context, docID, key string, c changelog.Commit) error {
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

// CommitsAfter returns docID's commits strictly after afterID, oldest first
// (replay order). afterID "" means from the root. limit <= 0 means all. An
// afterID not on docID returns changelog.ErrNoSuchCommit. Ties within the same
// DateTime64(6) microsecond break on id, mirroring Commits/Head's ORDER BY.
func (l *Log) CommitsAfter(ctx context.Context, docID, afterID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := `SELECT id, parent, at, authors, message, changes FROM commits FINAL WHERE doc_id = ?`
	args := []any{docID}
	if afterID != "" {
		var at time.Time
		var id string
		err := l.db.QueryRowContext(ctx,
			`SELECT at, id FROM commits FINAL WHERE doc_id = ? AND id = ?`, docID, afterID).Scan(&at, &id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, changelog.ErrNoSuchCommit
		}
		if err != nil {
			return nil, fmt.Errorf("changelog-clickhouse: commits after: resolve cursor: %w", err)
		}
		q += ` AND (at, id) > (?, ?)`
		args = append(args, at, id)
	}
	q += ` ORDER BY at ASC, id ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-clickhouse: commits after: %w", err)
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

// PruneSeen deletes seen records older than olderThan. Unlike the SQL adapter,
// this reports no affected row count: ClickHouse lightweight deletes are
// mutations, applied asynchronously in the background, so there is nothing
// synchronous to count. A native TTL clause on the seen table (`TTL at +
// INTERVAL ...`) is the alternative for fresh installs. Whichever mechanism is
// used, retention must exceed the producer's max redelivery window, or a
// legitimate retry can land as unseen and be double-sealed.
func (l *Log) PruneSeen(ctx context.Context, olderThan time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := l.db.ExecContext(ctx, `DELETE FROM seen WHERE at < ?`, olderThan.UTC()); err != nil {
		return fmt.Errorf("changelog-clickhouse: prune seen: %w", err)
	}
	return nil
}

// SaveSnapshot stores s, replacing any prior snapshot for s.DocID.
// ReplacingMergeTree reconciles the replacement at read time via FINAL.
func (l *Log) SaveSnapshot(ctx context.Context, s changelog.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO snapshots (doc_id, commit_id, state, at) VALUES (?, ?, ?, ?)`,
		s.DocID, s.CommitID, string(s.State), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: save snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot returns the stored snapshot for docID; ok is false if none.
func (l *Log) LoadSnapshot(ctx context.Context, docID string) (changelog.Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Snapshot{}, false, err
	}
	var commitID, state string
	err := l.db.QueryRowContext(ctx,
		`SELECT commit_id, state FROM snapshots FINAL WHERE doc_id = ? LIMIT 1`, docID).Scan(&commitID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return changelog.Snapshot{}, false, nil
	}
	if err != nil {
		return changelog.Snapshot{}, false, fmt.Errorf("changelog-clickhouse: load snapshot: %w", err)
	}
	return changelog.Snapshot{DocID: docID, CommitID: commitID, State: []byte(state)}, true, nil
}

// SaveAnnotation stores a, replacing any prior annotation for
// (a.DocID, a.CommitID). ReplacingMergeTree reconciles the replacement at read
// time via FINAL, `at` as the version column.
func (l *Log) SaveAnnotation(ctx context.Context, a changelog.Annotation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO annotations (doc_id, commit_id, data, at) VALUES (?, ?, ?, ?)`,
		a.DocID, a.CommitID, string(a.Data), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("changelog-clickhouse: save annotation: %w", err)
	}
	return nil
}

// LoadAnnotations returns docID's annotations for the given commit ids, keyed
// by commit id; ids without one are absent.
func (l *Log) LoadAnnotations(ctx context.Context, docID string, commitIDs []string) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	if len(commitIDs) == 0 {
		return out, nil
	}
	q := `SELECT commit_id, data FROM annotations FINAL WHERE doc_id = ? AND commit_id IN (?` +
		strings.Repeat(", ?", len(commitIDs)-1) + `)`
	args := make([]any, 0, len(commitIDs)+1)
	args = append(args, docID)
	for _, id := range commitIDs {
		args = append(args, id)
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("changelog-clickhouse: load annotations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("changelog-clickhouse: load annotations: %w", err)
		}
		out[id] = []byte(data)
	}
	return out, rows.Err()
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
