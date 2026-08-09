// Package memlog is a minimal in-memory changelog.Log for the kit's own tests.
//
// It exists so the kit module depends on core only (not on an adapter), and so
// the same stub can be shared by test packages across the kit's subpackages —
// Go cannot share _test.go files between packages. It lives under internal/ and
// is therefore invisible to consumers; use adapters/memory for anything real.
package memlog

import (
	"context"
	"sort"
	"sync"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// Log is a minimal in-memory Log+Indexer+Deduper+TailReader+Snapshotter.
// Mirrors core's own test stub, plus call counters for the snapshot fast-path
// tests.
type Log struct {
	mu      sync.Mutex
	commits map[string][]changelog.Commit
	seen    map[[2]string]changelog.Commit
	snaps   map[string]changelog.Snapshot

	commitsCalls int // full-history Commits reads
	afterCalls   int // cursor CommitsAfter reads
}

// New returns an empty Log.
func New() *Log {
	return &Log{
		commits: map[string][]changelog.Commit{},
		seen:    map[[2]string]changelog.Commit{},
		snaps:   map[string]changelog.Snapshot{},
	}
}

// NewService returns a Service over a fresh Log.
func NewService() changelog.Service {
	return changelog.NewService(New())
}

func (m *Log) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[docID] = append(m.commits[docID], c)
	return nil
}

func (m *Log) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commitsCalls++
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

func (m *Log) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []changelog.DocCommit{}
	ids := make([]string, 0, len(m.commits))
	for d := range m.commits {
		ids = append(ids, d)
	}
	sort.Strings(ids)
	for _, id := range ids {
		src := m.commits[id]
		for i := len(src) - 1; i >= 0; i-- {
			out = append(out, changelog.DocCommit{DocID: id, Commit: src[i]})
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Log) FindByID(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.DocCommit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, src := range m.commits {
		for _, c := range src {
			if c.ID == commitID {
				return changelog.DocCommit{DocID: id, Commit: c}, true, nil
			}
		}
	}
	return changelog.DocCommit{}, false, nil
}

func (m *Log) Seen(ctx context.Context, docID, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.seen[[2]string{docID, key}]
	return c, ok, nil
}

func (m *Log) MarkSeen(ctx context.Context, docID, key string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{docID, key}
	if _, ok := m.seen[k]; !ok {
		m.seen[k] = c
	}
	return nil
}

func (m *Log) CommitsAfter(ctx context.Context, docID, afterID string, limit int) ([]changelog.Commit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.afterCalls++
	src := m.commits[docID]
	start := 0
	if afterID != "" {
		start = -1
		for i, c := range src {
			if c.ID == afterID {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, changelog.ErrNoSuchCommit
		}
	}
	tail := src[start:]
	if limit > 0 && len(tail) > limit {
		tail = tail[:limit]
	}
	return append([]changelog.Commit(nil), tail...), nil
}

func (m *Log) SaveSnapshot(ctx context.Context, s changelog.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.State = append([]byte(nil), s.State...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snaps[s.DocID] = s
	return nil
}

func (m *Log) LoadSnapshot(ctx context.Context, docID string) (changelog.Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Snapshot{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snaps[docID]
	if !ok {
		return changelog.Snapshot{}, false, nil
	}
	s.State = append([]byte(nil), s.State...)
	return s, true, nil
}

// Snapshots returns a copy of the stored snapshots, for tests asserting on what
// the snapshot cache holds. Use SaveSnapshot to seed one.
func (m *Log) Snapshots() map[string]changelog.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]changelog.Snapshot, len(m.snaps))
	for k, v := range m.snaps {
		v.State = append([]byte(nil), v.State...)
		out[k] = v
	}
	return out
}

// Counters returns a consistent view of the read counters.
func (m *Log) Counters() (commitsCalls, afterCalls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commitsCalls, m.afterCalls
}
