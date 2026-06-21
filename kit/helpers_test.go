package chroniclekit

import (
	"context"
	"sort"
	"sync"
	"testing"

	changelog "github.com/zdirnecamlcs96/chronicle/core"
)

// memLog is a minimal in-memory Log+Indexer+Deduper for kit tests, so the kit's
// module depends on core only (not on an adapter). Mirrors core's own test stub.
type memLog struct {
	mu      sync.Mutex
	commits map[string][]changelog.Commit
	seen    map[[2]string]changelog.Commit
}

func newMemService() changelog.Service {
	return changelog.NewService(&memLog{
		commits: map[string][]changelog.Commit{},
		seen:    map[[2]string]changelog.Commit{},
	})
}

func (m *memLog) AppendCommit(ctx context.Context, docID string, c changelog.Commit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[docID] = append(m.commits[docID], c)
	return nil
}

func (m *memLog) Commits(ctx context.Context, docID string, limit int) ([]changelog.Commit, error) {
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

func (m *memLog) Head(ctx context.Context, docID string) (string, error) {
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

func (m *memLog) AllCommits(ctx context.Context, limit int) ([]changelog.DocCommit, error) {
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

func (m *memLog) FindByID(ctx context.Context, commitID string) (changelog.DocCommit, bool, error) {
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

func (m *memLog) Seen(ctx context.Context, docID, key string) (changelog.Commit, bool, error) {
	if err := ctx.Err(); err != nil {
		return changelog.Commit{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.seen[[2]string{docID, key}]
	return c, ok, nil
}

func (m *memLog) MarkSeen(ctx context.Context, docID, key string, c changelog.Commit) error {
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

// norm JSON-normalizes v for comparison with reconstructed/snapshot values.
func norm(t *testing.T, v any) any {
	t.Helper()
	out, err := normalize(v)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}
