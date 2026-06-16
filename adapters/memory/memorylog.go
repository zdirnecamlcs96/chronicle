// Package changelogmemory provides the in-memory reference Log — the built-in
// fallback adapter. It is ephemeral (history is lost on restart) and NOT for
// production: AppendCommit stores whatever parent the Recorder computed, so
// concurrent same-document appends can fork (it does not pass
// RunSerializableAppend). Use a durable adapter (adapters/sql,
// adapters/clickhouse) in production; this is the zero-config default for
// development, examples, and the conformance suite's reference backend.
package changelogmemory

import (
	"context"
	"sync"

	"github.com/zdirnecamlcs96/chronicle/core"
)

// Log is the in-memory changelog.Log.
type Log struct {
	mu      sync.Mutex
	commits map[string][]changelog.Commit
}

// New returns an empty in-memory Log.
func New() *Log {
	return &Log{commits: map[string][]changelog.Commit{}}
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

var _ changelog.Log = (*Log)(nil)
