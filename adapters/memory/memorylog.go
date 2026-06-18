// Package changelogmemory provides the in-memory reference Log. It implements
// changelog.Log plus the optional changelog.Indexer and changelog.Deduper
// capabilities (cross-document queries and per-document idempotency), all in
// process. It is ephemeral (history is lost on restart) and NOT for production:
// AppendCommit stores whatever parent the Recorder computed, so concurrent
// same-document appends can fork (it does not pass RunSerializableAppend). Use a
// durable adapter (adapters/sql, adapters/clickhouse) in production; this is the
// zero-config default for development, examples, and the conformance suite's
// reference backend.
package changelogmemory

import (
	"context"
	"sort"
	"sync"

	"github.com/zdirnecamlcs96/chronicle/core"
)

// Log is the in-memory changelog.Log.
type Log struct {
	mu      sync.Mutex
	commits map[string][]changelog.Commit
	seen    map[seenKey]changelog.Commit
}

// seenKey scopes an idempotency key to its document, so a key reused on a
// different document is a distinct delivery and never replays another
// document's commit.
type seenKey struct{ docID, key string }

// New returns an empty in-memory Log.
func New() *Log {
	return &Log{
		commits: map[string][]changelog.Commit{},
		seen:    map[seenKey]changelog.Commit{},
	}
}

// AppendCommit stores c in document docID's append-ordered history.
func (m *Log) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[docID] = append(m.commits[docID], c)
	return nil
}

// Commits returns docID's commits newest first, capped at limit (0 = all).
func (m *Log) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.commits[docID]
	out := make([]changelog.Commit, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- {
		out = append(out, src[i])
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Head returns docID's most recent commit ID, or "" when there are none.
func (m *Log) Head(ctx context.Context, docID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.commits[docID]
	if len(src) == 0 {
		return "", nil
	}
	return src[len(src)-1].ID, nil
}

// Seen returns the commit (docID, key) previously sealed; keys are scoped per
// document. ok is false if unseen.
func (m *Log) Seen(ctx context.Context, docID, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.seen[seenKey{docID, key}]
	return c, ok, nil
}

// MarkSeen records that key sealed c for docID. First (docID, key) writer wins.
func (m *Log) MarkSeen(ctx context.Context, docID, key string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := seenKey{docID, key}
	if _, ok := m.seen[k]; !ok {
		m.seen[k] = c
	}
	return nil
}

// AllCommits returns commits across all documents, newest first. limit <= 0 means all.
func (m *Log) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []changelog.DocCommit{}
	for _, docID := range m.sortedDocIDs() {
		src := m.commits[docID]
		for i := len(src) - 1; i >= 0; i-- { // newest first within a document
			out = append(out, changelog.DocCommit{DocID: docID, Commit: src[i]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Commit.At.After(out[j].Commit.At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// FindByID returns the commit with the given id and its document; ok is false if
// none. Documents are scanned in id order, so a content hash shared by two
// documents resolves deterministically (first by document id wins).
func (m *Log) FindByID(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.DocCommit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, docID := range m.sortedDocIDs() {
		for _, c := range m.commits[docID] {
			if c.ID == commitID {
				return changelog.DocCommit{DocID: docID, Commit: c}, true, nil
			}
		}
	}
	return changelog.DocCommit{}, false, nil
}

// sortedDocIDs returns the document ids in ascending order for deterministic
// cross-document iteration. Callers must hold m.mu.
func (m *Log) sortedDocIDs() []string {
	ids := make([]string, 0, len(m.commits))
	for d := range m.commits {
		ids = append(ids, d)
	}
	sort.Strings(ids)
	return ids
}

var (
	_ changelog.Log     = (*Log)(nil)
	_ changelog.Indexer = (*Log)(nil)
	_ changelog.Deduper = (*Log)(nil)
)
